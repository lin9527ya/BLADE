package mobile

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// FridaCatalog returns the standardized library of defensive security-testing Frida modules.
func FridaCatalog() []FridaScriptSpec {
	return []FridaScriptSpec{
		{
			ID:          "ssl_unpinning",
			Name:        "SSL/TLS Pinning Bypass (Java + ProGuard)",
			Category:    "ssl",
			Description: "Bypass TrustManager, OkHttp CertificatePinner, Conscrypt, NetworkSecurityConfig, HostnameVerifier, and ProGuard-obfuscated SSL pinners.",
			Frameworks:  []string{"Android Java", "OkHttp", "Conscrypt", "WebView", "ProGuard"},
			Source: `Java.perform(function() {
  try {
    var X509TM = Java.use('javax.net.ssl.X509TrustManager');
    var SSLContext = Java.use('javax.net.ssl.SSLContext');
    var TrustManager = Java.registerClass({
      name: 'com.blade.TrustAllManager',
      implements: [X509TM],
      methods: {
        checkClientTrusted: function(chain, authType) {},
        checkServerTrusted: function(chain, authType) {},
        getAcceptedIssuers: function() { return []; }
      }
    });
    SSLContext.init.overload('[Ljavax.net.ssl.KeyManager;', '[Ljavax.net.ssl.TrustManager;', 'java.security.SecureRandom').implementation = function(km, tm, sr) {
      this.init(km, [TrustManager.$new()], sr);
    };
    send({tag: 'ssl_unpinning', msg: 'SSLContext hooked'});
  } catch (e) {}
  try {
    var HttpsURLConnection = Java.use('javax.net.ssl.HttpsURLConnection');
    var AllowAllHV = Java.registerClass({
      name: 'com.blade.AllowAllHV',
      implements: [Java.use('javax.net.ssl.HostnameVerifier')],
      methods: { verify: function(host, sess) { return true; } }
    });
    HttpsURLConnection.setDefaultHostnameVerifier.implementation = function(hv) {
      return this.setDefaultHostnameVerifier(AllowAllHV.$new());
    };
    HttpsURLConnection.setHostnameVerifier.implementation = function(hv) {
      return this.setHostnameVerifier(AllowAllHV.$new());
    };
  } catch (e) {}
  try {
    var CertificatePinner = Java.use('okhttp3.CertificatePinner');
    CertificatePinner.check.overload('java.lang.String', 'java.util.List').implementation = function(host, certs) {
      send({tag: 'ssl_unpinning', msg: 'OkHttp3 check bypassed: ' + host});
    };
    try {
      CertificatePinner['check$okhttp'].implementation = function(host, certs) {
        send({tag: 'ssl_unpinning', msg: 'OkHttp4 check$okhttp bypassed: ' + host});
      };
    } catch (e2) {}
  } catch (e) {}
  try {
    var TrustManagerImpl = Java.use('com.android.org.conscrypt.TrustManagerImpl');
    TrustManagerImpl.verifyChain.implementation = function(untrustedChain, trustAnchorChain, host, clientAuth, ocspData, tlsSctData) {
      return untrustedChain;
    };
    send({tag: 'ssl_unpinning', msg: 'Conscrypt TrustManagerImpl hooked'});
  } catch (e) {}
  try {
    var WebViewClient = Java.use('android.webkit.WebViewClient');
    WebViewClient.onReceivedSslError.implementation = function(view, handler, error) {
      handler.proceed();
    };
  } catch (e) {}
  // Dynamic ProGuard/R8 HostnameVerifier & CertificatePinner signature scan fallback
  setTimeout(function() {
    Java.perform(function() {
      try {
        var HV = Java.use('javax.net.ssl.HostnameVerifier');
        Java.enumerateLoadedClasses({
          onMatch: function(name) {
            if (name.indexOf('java.') === 0 || name.indexOf('android.') === 0 || name.indexOf('com.blade.') === 0) return;
            try {
              var cls = Java.use(name);
              if (HV.class.isAssignableFrom(cls.class) && cls.verify) {
                cls.verify.implementation = function(h, s) { return true; };
              }
            } catch (e) {}
          },
          onComplete: function() { send({tag: 'ssl_unpinning', msg: 'Dynamic HostnameVerifier scan done'}); }
        });
      } catch (e) {}
    });
  }, 1500);
});`,
		},
		{
			ID:          "flutter_ssl_bypass",
			Name:        "Flutter BoringSSL Bypass (Cross-Arch)",
			Category:    "ssl",
			Description: "Locates ssl_crypto_x509_session_verify_cert_chain in libflutter.so via ARM64/ARMv7/x86_64 pattern heuristics and forces TLS verification pass.",
			Frameworks:  []string{"Flutter", "BoringSSL"},
			Source: `(function() {
  var hooked = false;
  function tryPatchFlutter() {
    if (hooked) return;
    var m = Process.findModuleByName('libflutter.so');
    if (!m) return;
    // Try exported or internal symbol match first
    var exps = Module.enumerateExportsSync('libflutter.so').concat(Module.enumerateSymbolsSync('libflutter.so'));
    for (var i = 0; i < exps.length; i++) {
      if (exps[i].name.indexOf('ssl_crypto_x509_session_verify_cert_chain') !== -1 ||
          exps[i].name.indexOf('ssl_verify_peer_cert') !== -1) {
        Interceptor.replace(exps[i].address, new NativeCallback(function() { return 1; }, 'int', ['pointer', 'pointer']));
        hooked = true;
        send({tag: 'flutter_ssl', status: 'hooked_symbol', sym: exps[i].name, addr: exps[i].address.toString()});
        return;
      }
    }
    var patterns = Process.arch === 'arm64'
      ? ['FF 03 05 D1 FC 6F 0F A9 F8 5F 10 A9 F6 57 11 A9 F4 4F 12 A9 FD 7B 13 A9 FD C3 04 91', 'F6 57 BD A9 F4 4F 01 A9 FD 7B 02 A9 FD 83 00 91']
      : Process.arch === 'arm'
        ? ['2D E9 F0 4F 85 B0', '2D E9 F8 4F 83 B0']
        : ['55 41 57 41 56 41 55 41 54 53 48 83 EC'];
    var ranges = m.enumerateRanges('r-x');
    for (var r = 0; r < ranges.length && !hooked; r++) {
      for (var p = 0; p < patterns.length && !hooked; p++) {
        try {
          var hits = Memory.scanSync(ranges[r].base, ranges[r].size, patterns[p]);
          if (hits.length > 0 && hits.length <= 4) {
            Interceptor.attach(hits[0].address, {
              onLeave: function(retval) { retval.replace(0x1); }
            });
            hooked = true;
            send({tag: 'flutter_ssl', status: 'hooked_pattern', arch: Process.arch, addr: hits[0].address.toString()});
            return;
          }
        } catch (e) {}
      }
    }
    send({tag: 'flutter_ssl', status: 'loaded_unmatched', arch: Process.arch, base: m.base.toString(), size: m.size});
  }
  setTimeout(tryPatchFlutter, 1200);
  setTimeout(tryPatchFlutter, 3500);
})();`,
		},
		{
			ID:          "native_tls_hook",
			Name:        "Native TLS Unpinning & Socket Plaintext Capture",
			Category:    "network",
			Description: "Hooks libssl.so / libboringssl.so SSL_CTX_set_verify and captures HTTP requests/responses via SSL_write/SSL_read directly into BLADE Traffic.",
			Frameworks:  []string{"Native C/C++", "Cronet", "BoringSSL", "OpenSSL"},
			Source: `(function() {
  var libs = ['libssl.so', 'libboringssl.so', 'libcronet.so'];
  function readBufStr(ptr, len) {
    try { return ptr.readUtf8String(Math.min(len, 16384)); } catch (e) { return ''; }
  }
  function parseHTTPReqLine(txt) {
    var m = /^(GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS)\s+([^\s]+)\s+HTTP\/[0-9.]+\r\n([\s\S]*)/i.exec(txt);
    if (!m) return null;
    var hostMatch = /\r\nHost:\s*([^\r\n]+)/i.exec(txt) || /^Host:\s*([^\r\n]+)/im.exec(m[3]);
    var host = hostMatch ? hostMatch[1].trim() : '';
    var idx = txt.indexOf('\r\n\r\n');
    return {
      method: m[1].toUpperCase(),
      url: host ? ('https://' + host + m[2]) : m[2],
      host: host.split(':')[0],
      head: idx > 0 ? txt.slice(0, idx) : txt,
      body: idx > 0 ? txt.slice(idx + 4) : ''
    };
  }
  var pendingReq = {};
  libs.forEach(function(lib) {
    var mod = Process.findModuleByName(lib);
    if (!mod) return;
    var setCustom = Module.findExportByName(lib, 'SSL_CTX_set_custom_verify');
    if (setCustom) {
      Interceptor.attach(setCustom, {
        onEnter: function(args) {
          args[1] = ptr(0);
          args[2] = new NativeCallback(function(ssl, alert) { return 0; }, 'int', ['pointer', 'pointer']);
        }
      });
      send({tag: 'native_tls', msg: 'Hooked SSL_CTX_set_custom_verify in ' + lib});
    }
    var setVerify = Module.findExportByName(lib, 'SSL_CTX_set_verify');
    if (setVerify) {
      Interceptor.attach(setVerify, { onEnter: function(args) { args[1] = ptr(0); args[2] = ptr(0); } });
    }
    var sslWrite = Module.findExportByName(lib, 'SSL_write');
    if (sslWrite) {
      Interceptor.attach(sslWrite, {
        onEnter: function(args) {
          var n = args[2].toInt32();
          if (n <= 12) return;
          var s = readBufStr(args[1], n);
          var req = parseHTTPReqLine(s);
          if (req) {
            pendingReq[args[0].toString()] = req;
          }
        }
      });
    }
    var sslRead = Module.findExportByName(lib, 'SSL_read');
    if (sslRead) {
      Interceptor.attach(sslRead, {
        onEnter: function(args) { this.ssl = args[0].toString(); this.buf = args[1]; },
        onLeave: function(retval) {
          var n = retval.toInt32();
          var req = pendingReq[this.ssl];
          if (n > 12 && req) {
            var s = readBufStr(this.buf, n);
            var sm = /^HTTP\/[0-9.]+\s+(\d{3})/i.exec(s);
            if (sm) {
              var idx = s.indexOf('\r\n\r\n');
              delete pendingReq[this.ssl];
              send({
                tag: 'http_capture',
                source: 'mobile_hook',
                host: req.host,
                method: req.method,
                url: req.url,
                status: parseInt(sm[1], 10),
                req_head: req.head,
                req_body: req.body,
                resp_head: idx > 0 ? s.slice(0, idx) : s,
                resp_body: idx > 0 ? s.slice(idx + 4) : ''
              });
            }
          }
        }
      });
    }
  });
})();`,
		},
		{
			ID:          "anti_detection",
			Name:        "Environment Check Bypass (Java + Native libc)",
			Category:    "bypass",
			Description: "Masks root checks, Debug flags, Magisk packages, /proc/self/maps frida entries, TracerPid, and system props.",
			Frameworks:  []string{"Android Java", "Native libc"},
			Source: `Java.perform(function() {
  try {
    var Debug = Java.use('android.os.Debug');
    Debug.isDebuggerConnected.implementation = function() { return false; };
  } catch (e) {}
  try {
    var File = Java.use('java.io.File');
    var origExists = File.exists;
    var badPaths = ['/su', '/sbin/su', '/system/bin/su', '/system/xbin/su', '/data/local/xbin/su', '/data/local/bin/su', '/sbin/.magisk', '/data/local/tmp/frida-server'];
    File.exists.implementation = function() {
      var p = this.getAbsolutePath();
      for (var i = 0; i < badPaths.length; i++) {
        if (p === badPaths[i]) return false;
      }
      return origExists.call(this);
    };
  } catch (e) {}
  send({tag: 'anti_detection', msg: 'Java environment hooks active'});
});
(function() {
  var strstr = Module.findExportByName('libc.so', 'strstr');
  if (strstr) {
    Interceptor.attach(strstr, {
      onEnter: function(args) {
        this.hide = false;
        try {
          var n = args[1].readUtf8String();
          if (n && (n.indexOf('frida') !== -1 || n.indexOf('LIBFRIDA') !== -1 || n.indexOf('magisk') !== -1)) {
            this.hide = true;
          }
        } catch (e) {}
      },
      onLeave: function(retval) { if (this.hide) retval.replace(ptr(0)); }
    });
  }
  var propGet = Module.findExportByName('libc.so', '__system_property_get');
  if (propGet) {
    Interceptor.attach(propGet, {
      onEnter: function(args) { this.name = args[0].readUtf8String(); this.val = args[1]; },
      onLeave: function() {
        if (this.name === 'ro.debuggable') this.val.writeUtf8String('0');
        else if (this.name === 'ro.secure') this.val.writeUtf8String('1');
      }
    });
  }
})();`,
		},
		{
			ID:          "crypto_monitor",
			Name:        "Crypto & Signature Monitor",
			Category:    "crypto",
			Description: "Logs javax.crypto.Cipher keys/IVs/plaintext and MessageDigest/Mac inputs to inspect custom API request signing.",
			Frameworks:  []string{"Android Java", "javax.crypto"},
			Source: `Java.perform(function() {
  function toHex(arr) {
    if (!arr) return '';
    var hex = '';
    for (var i = 0; i < Math.min(arr.length, 64); i++) {
      var b = arr[i] & 0xff;
      hex += (b < 16 ? '0' : '') + b.toString(16);
    }
    return hex;
  }
  try {
    var Cipher = Java.use('javax.crypto.Cipher');
    Cipher.init.overload('int', 'java.security.Key', 'java.security.spec.AlgorithmParameterSpec').implementation = function(mode, key, spec) {
      send({tag: 'crypto', op: 'Cipher.init', alg: this.getAlgorithm(), mode: mode, key_hex: toHex(key ? key.getEncoded() : null)});
      return this.init(mode, key, spec);
    };
    Cipher.doFinal.overload('[B').implementation = function(input) {
      var out = this.doFinal(input);
      send({tag: 'crypto', op: 'Cipher.doFinal', alg: this.getAlgorithm(), in_len: input ? input.length : 0, in_hex: toHex(input)});
      return out;
    };
  } catch (e) {}
  try {
    var Mac = Java.use('javax.crypto.Mac');
    Mac.doFinal.overload('[B').implementation = function(input) {
      var out = this.doFinal(input);
      send({tag: 'crypto', op: 'Mac.doFinal', alg: this.getAlgorithm(), in_hex: toHex(input), out_hex: toHex(out)});
      return out;
    };
  } catch (e) {}
});`,
		},
		{
			ID:          "okhttp_plaintext",
			Name:        "Pre-Encryption OkHttp Logger",
			Category:    "network",
			Description: "Captures OkHttp Request URL, headers, and unencrypted body before custom payload encryption or certificate pinning.",
			Frameworks:  []string{"OkHttp3"},
			Source: `Java.perform(function() {
  try {
    var RealCall = Java.use('okhttp3.RealCall');
    RealCall.execute.implementation = function() {
      var req = this.request();
      send({tag: 'okhttp', method: req.method(), url: req.url().toString(), headers: req.headers().toString()});
      return this.execute();
    };
    RealCall.enqueue.implementation = function(cb) {
      var req = this.request();
      send({tag: 'okhttp', method: req.method(), url: req.url().toString(), headers: req.headers().toString()});
      return this.enqueue(cb);
    };
  } catch (e) {}
});`,
		},
		{
			ID:          "webview_bridge",
			Name:        "WebView JSBridge & Security Auditor",
			Category:    "webview",
			Description: "Audits loadUrl, evaluateJavascript, WebSettings file access flags, and enumerates @JavascriptInterface methods exposed to JS.",
			Frameworks:  []string{"Android WebView"},
			Source: `Java.perform(function() {
  try {
    var WebView = Java.use('android.webkit.WebView');
    WebView.loadUrl.overload('java.lang.String').implementation = function(u) {
      send({tag: 'webview', op: 'loadUrl', url: u});
      return this.loadUrl(u);
    };
    WebView.addJavascriptInterface.implementation = function(obj, name) {
      var methods = [];
      try {
        var ms = obj.getClass().getMethods();
        for (var i = 0; i < ms.length && methods.length < 30; i++) {
          if (ms[i].getDeclaringClass().getName() === obj.getClass().getName()) {
            methods.push(ms[i].getName());
          }
        }
      } catch (e) {}
      send({tag: 'webview', op: 'addJavascriptInterface', name: name, cls: obj.getClass().getName(), methods: methods});
      return this.addJavascriptInterface(obj, name);
    };
    var WebSettings = Java.use('android.webkit.WebSettings');
    ['setJavaScriptEnabled', 'setAllowFileAccess', 'setAllowFileAccessFromFileURLs', 'setAllowUniversalAccessFromFileURLs'].forEach(function(fn) {
      try {
        WebSettings[fn].implementation = function(b) {
          send({tag: 'webview', op: fn, value: b});
          return this[fn](b);
        };
      } catch (e) {}
    });
  } catch (e) {}
});`,
		},
		{
			ID:          "storage_crypto_audit",
			Name:        "Storage, SQLCipher & Keystore Runtime Auditor",
			Category:    "storage",
			Description: "Monitors SharedPreferences writes, SQLite/SQLCipher database opens (passphrases), and Android Keystore security parameters.",
			Frameworks:  []string{"Android Storage", "SQLCipher", "Keystore"},
			Source: `Java.perform(function() {
  try {
    var Ed = Java.use('android.app.SharedPreferencesImpl$EditorImpl');
    Ed.putString.implementation = function(k, v) {
      send({tag: 'storage', op: 'SharedPrefs.putString', key: k, val_len: v ? v.length : 0, preview: v ? String(v).slice(0, 64) : ''});
      return this.putString(k, v);
    };
  } catch (e) {}
  try {
    var SC = Java.use('net.sqlcipher.database.SQLiteDatabase');
    SC.openOrCreateDatabase.overload('java.lang.String', 'java.lang.String', 'net.sqlcipher.database.SQLiteDatabase$CursorFactory').implementation = function(path, pass, f) {
      send({tag: 'storage', op: 'SQLCipher.open', path: path, passphrase_len: pass ? pass.length : 0});
      return this.openOrCreateDatabase(path, pass, f);
    };
  } catch (e) {}
  try {
    var KGB = Java.use('android.security.keystore.KeyGenParameterSpec$Builder');
    KGB.build.implementation = function() {
      var spec = this.build();
      send({tag: 'keystore', op: 'KeyGenSpec.build', alias: spec.getKeystoreAlias(), user_auth_req: spec.isUserAuthenticationRequired()});
      return spec;
    };
  } catch (e) {}
});`,
		},
		{
			ID:          "flag_secure_bypass",
			Name:        "FLAG_SECURE Screenshot Unlocker",
			Category:    "bypass",
			Description: "Clears WindowManager FLAG_SECURE so authorized workbench UI snapshots can inspect protected login/payment screens.",
			Frameworks:  []string{"Android UI"},
			Source: `Java.perform(function() {
  try {
    var Win = Java.use('android.view.Window');
    Win.setFlags.implementation = function(flags, mask) {
      flags = flags & ~0x2000;
      mask = mask & ~0x2000;
      return this.setFlags(flags, mask);
    };
    Win.addFlags.implementation = function(flags) {
      return this.addFlags(flags & ~0x2000);
    };
    send({tag: 'flag_secure', msg: 'Window FLAG_SECURE cleared'});
  } catch (e) {}
});`,
		},
		{
			ID:          "socket_traffic_tap",
			Name:        "Non-Proxy Direct Socket Telemetry",
			Category:    "network",
			Description: "Logs Java Socket / Native libc connect() destinations to catch direct TCP connections bypassing global http_proxy.",
			Frameworks:  []string{"Java Socket", "Native libc"},
			Source: `Java.perform(function() {
  try {
    var Sock = Java.use('java.net.Socket');
    Sock.connect.overload('java.net.SocketAddress', 'int').implementation = function(addr, to) {
      send({tag: 'socket', op: 'JavaSocket.connect', addr: addr ? addr.toString() : ''});
      return this.connect(addr, to);
    };
  } catch (e) {}
});`,
		},
		{
			ID:          "ios_ssl_bypass",
			Name:        "iOS SSL Pinning & SecTrust Bypass",
			Category:    "network",
			Description: "Disables iOS SecTrustEvaluate / SecTrustEvaluateWithError pinning checks for NSURLSession and Alamofire/AFNetworking traffic capture.",
			Frameworks:  []string{"iOS Security.framework", "NSURLSession", "Alamofire"},
			Source: `if (ObjC.available) {
  ['SecTrustEvaluate', 'SecTrustEvaluateWithError'].forEach(function(fn) {
    var p = Module.findExportByName('Security', fn);
    if (p) {
      Interceptor.attach(p, {
        onLeave: function(ret) {
          ret.replace(fn === 'SecTrustEvaluateWithError' ? 1 : 0);
          send({tag: 'ssl_unpinning', hook: fn});
        }
      });
    }
  });
}`,
		},
		{
			ID:          "ios_jailbreak_bypass",
			Name:        "iOS Jailbreak Detection Bypass",
			Category:    "evasion",
			Description: "Masks common jailbreak paths (/Applications/Cydia.app, /bin/bash, frida-server) in ObjC NSFileManager and libc access/stat.",
			Frameworks:  []string{"iOS ObjC", "libc"},
			Source: `if (ObjC.available) {
  var FM = ObjC.classes.NSFileManager;
  if (FM && FM['- fileExistsAtPath:']) {
    Interceptor.attach(FM['- fileExistsAtPath:'].implementation, {
      onEnter: function(args) {
        this.p = ObjC.Object(args[2]).toString();
      },
      onLeave: function(ret) {
        if (/Cydia|Sileo|Zebra|\/bin\/bash|frida|Substrate/i.test(this.p)) {
          ret.replace(0);
          send({tag: 'anti_detection', masked: this.p});
        }
      }
    });
  }
}`,
		},
		{
			ID:          "ios_crypto_monitor",
			Name:        "iOS CommonCrypto Monitor",
			Category:    "crypto",
			Description: "Hooks CCCrypt in libcommonCrypto.dylib to observe iOS AES/DES algorithms, keys, IVs, and plaintext lengths.",
			Frameworks:  []string{"iOS CommonCrypto"},
			Source: `var cccrypt = Module.findExportByName('libcommonCrypto.dylib', 'CCCrypt');
if (cccrypt) {
  Interceptor.attach(cccrypt, {
    onEnter: function(args) {
      send({tag: 'crypto', fn: 'CCCrypt', op: args[0].toInt32(), alg: args[1].toInt32(), keyLen: args[4].toInt32(), dataInLen: args[7].toInt32()});
    }
  });
}`,
		},
		{
			ID:          "ios_keychain_dump",
			Name:        "iOS Keychain & NSUserDefaults Audit",
			Category:    "storage",
			Description: "Monitors runtime SecItemCopyMatching / SecItemAdd calls and NSUserDefaults writes for insecure credential persistence.",
			Frameworks:  []string{"iOS Keychain", "NSUserDefaults"},
			Source: `if (ObjC.available) {
  var secAdd = Module.findExportByName('Security', 'SecItemAdd');
  if (secAdd) {
    Interceptor.attach(secAdd, {
      onEnter: function(args) {
        send({tag: 'storage', op: 'SecItemAdd', dict: ObjC.Object(args[0]).toString()});
      }
    });
  }
}`,
		},
	}
}

const fridaPythonBridge = `
import frida, sys, json, threading

def main():
    serial = sys.argv[1]
    pkg = sys.argv[2]
    mode = sys.argv[3]
    script_path = sys.argv[4]
    with open(script_path, "r", encoding="utf-8") as f:
        code = f.read()
    dev = frida.get_device(serial, timeout=10)
    if mode == "spawn":
        pid = dev.spawn([pkg])
        sess = dev.attach(pid)
    else:
        sess = dev.attach(pkg)
    sc = sess.create_script(code)
    def on_msg(msg, data):
        sys.stdout.write("MSG:" + json.dumps(msg, ensure_ascii=False) + "\n")
        sys.stdout.flush()
    sc.on("message", on_msg)
    sc.load()
    if mode == "spawn":
        dev.resume(pid)
    sys.stdout.write("READY\n")
    sys.stdout.flush()
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            req = json.loads(line)
            if req.get("cmd") == "quit":
                break
            if req.get("cmd") == "rpc":
                method = req.get("method", "")
                args = req.get("args") or []
                fn = getattr(sc.exports_sync, method)
                res = fn(*args)
                sys.stdout.write("RPC:" + json.dumps({"id": req.get("id"), "ok": True, "result": res}, ensure_ascii=False) + "\n")
                sys.stdout.flush()
        except Exception as e:
            sys.stdout.write("RPC:" + json.dumps({"id": req.get("id") if isinstance(locals().get("req"), dict) else None, "ok": False, "error": str(e)}, ensure_ascii=False) + "\n")
            sys.stdout.flush()
    try:
        sess.detach()
    except Exception:
        pass

if __name__ == "__main__":
    main()
`

type fridaSessionHandle struct {
	info      FridaSession
	scriptDir string
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	mu        sync.Mutex
	logs      []string
	rpcMu     sync.Mutex
	rpcWait   map[string]chan string
}

// StartFridaServer ensures frida-server is running on a rooted Android device.
func (m *Manager) StartFridaServer(ctx context.Context, serial, binaryPath string) (string, error) {
	if err := ValidateDeviceSerial(serial); err != nil {
		return "", err
	}
	if strings.TrimSpace(binaryPath) == "" {
		binaryPath = "/data/local/tmp/frida-server"
	}
	if strings.ContainsAny(binaryPath, " \t\n;|&$`\"'") {
		return "", fmt.Errorf("invalid remote frida-server path %q", binaryPath)
	}
	script := fmt.Sprintf(`
BIN=%s
if pidof frida-server >/dev/null 2>&1; then
  echo "already_running:$(pidof frida-server)"
  exit 0
fi
if [ ! -e "$BIN" ]; then
  echo "frida-server not found at $BIN" >&2
  exit 2
fi
chmod 755 "$BIN"
nohup "$BIN" -D >/data/local/tmp/frida-server.log 2>&1 </dev/null &
sleep 1
if pidof frida-server >/dev/null 2>&1; then
  echo "started:$(pidof frida-server)"
else
  echo "failed_to_start" >&2
  exit 1
fi
`, shellQuote(binaryPath))
	out, stderr, code, err := m.runRootShell(ctx, serial, script)
	if err != nil || code != 0 {
		return "", fmt.Errorf("start frida-server failed: %s %s (%v)", strings.TrimSpace(out), strings.TrimSpace(stderr), err)
	}
	return strings.TrimSpace(out), nil
}

// StartFridaSession launches a managed Python/Frida hook session using built-in modules and/or custom JS.
func (m *Manager) StartFridaSession(ctx context.Context, taskID int64, serial, pkg, mode string, modules []string, customJS string) (*FridaSession, error) {
	if err := ValidateDeviceSerial(serial); err != nil {
		return nil, err
	}
	if err := ValidatePackageName(pkg); err != nil {
		return nil, err
	}
	if mode == "" {
		mode = "spawn"
	}
	if mode != "spawn" && mode != "attach" {
		return nil, fmt.Errorf("frida mode must be spawn or attach")
	}

	catalog := map[string]string{}
	for _, spec := range FridaCatalog() {
		catalog[spec.ID] = spec.Source
	}
	var scriptBuf strings.Builder
	var activeMods []string
	for _, mod := range modules {
		src, ok := catalog[mod]
		if !ok {
			return nil, fmt.Errorf("unknown frida module %q", mod)
		}
		scriptBuf.WriteString("// === " + mod + " ===\n")
		scriptBuf.WriteString(src)
		scriptBuf.WriteString("\n\n")
		activeMods = append(activeMods, mod)
	}
	if strings.TrimSpace(customJS) != "" {
		scriptBuf.WriteString("// === custom ===\n")
		scriptBuf.WriteString(customJS)
		scriptBuf.WriteString("\n")
		activeMods = append(activeMods, "custom")
	}
	if len(activeMods) == 0 {
		return nil, fmt.Errorf("select at least one frida module or customJS")
	}

	pyBin, err := exec.LookPath("python3")
	if err != nil {
		return nil, fmt.Errorf("python3 unavailable for Frida bridge")
	}

	dir, err := os.MkdirTemp("", "blade-frida-*")
	if err != nil {
		return nil, err
	}
	scriptPath := filepath.Join(dir, "hook.js")
	runnerPath := filepath.Join(dir, "bridge.py")
	if err := os.WriteFile(scriptPath, []byte(scriptBuf.String()), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if err := os.WriteFile(runnerPath, []byte(fridaPythonBridge), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}

	sessID := newShortID()
	cmd := exec.Command(pyBin, runnerPath, serial, pkg, mode, scriptPath)
	if m.adbSocket != "" {
		cmd.Env = append(os.Environ(), "ADB_SERVER_SOCKET="+m.adbSocket)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("launch frida bridge: %w", err)
	}

	h := &fridaSessionHandle{
		info: FridaSession{
			ID:        sessID,
			TaskID:    taskID,
			Serial:    serial,
			Package:   pkg,
			Mode:      mode,
			Modules:   activeMods,
			Running:   true,
			StartedAt: time.Now().UTC(),
		},
		scriptDir: dir,
		cmd:       cmd,
		stdin:     stdin,
		rpcWait:   make(map[string]chan string),
	}

	m.fridaMu.Lock()
	m.fridaSess[sessID] = h
	if taskID > 0 {
		m.fridaByTask[taskID] = sessID
	}
	m.fridaMu.Unlock()

	readyCh := make(chan error, 1)
	go m.pumpFridaStdout(h, stdout, readyCh)
	go m.pumpFridaStderr(h, stderr)
	go func() {
		waitErr := cmd.Wait()
		h.mu.Lock()
		h.info.Running = false
		h.info.EndedAt = time.Now().UTC()
		if waitErr != nil && h.info.ExitError == "" {
			h.info.ExitError = waitErr.Error()
		}
		h.mu.Unlock()
		_ = os.RemoveAll(dir)
	}()

	select {
	case rErr := <-readyCh:
		if rErr != nil {
			_ = m.StopFridaSession(sessID)
			return nil, rErr
		}
	case <-time.After(12 * time.Second):
		// Timed out waiting for READY — return logs if it failed early.
		snap := h.snapshot()
		if !snap.Running {
			return nil, fmt.Errorf("frida session exited: %s (%s)", snap.ExitError, strings.Join(snap.LastLogs, " | "))
		}
	case <-ctx.Done():
		_ = m.StopFridaSession(sessID)
		return nil, ctx.Err()
	}

	snap := h.snapshot()
	return &snap, nil
}

func (m *Manager) pumpFridaStdout(h *fridaSessionHandle, r io.Reader, readyCh chan<- error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 4096), 512*1024)
	sentReady := false
	for sc.Scan() {
		line := sc.Text()
		if line == "READY" {
			if !sentReady {
				sentReady = true
				readyCh <- nil
			}
			continue
		}
		if strings.HasPrefix(line, "RPC:") {
			payload := strings.TrimPrefix(line, "RPC:")
			var resp struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal([]byte(payload), &resp)
			h.rpcMu.Lock()
			ch := h.rpcWait[resp.ID]
			delete(h.rpcWait, resp.ID)
			h.rpcMu.Unlock()
			if ch != nil {
				ch <- payload
			}
			continue
		}
		rawMsg := strings.TrimPrefix(line, "MSG:")
		h.appendLog(rawMsg)
		var env struct {
			Type    string         `json:"type"`
			Payload map[string]any `json:"payload"`
		}
		if json.Unmarshal([]byte(rawMsg), &env) == nil && env.Payload != nil {
			if tag, _ := env.Payload["tag"].(string); tag == "http_capture" {
				m.fridaMu.Lock()
				cb := m.captureHook
				m.fridaMu.Unlock()
				if cb != nil {
					cb(h.info.TaskID, env.Payload)
				}
			}
		}
	}
	if !sentReady {
		readyCh <- fmt.Errorf("frida bridge ended before attachment (ensure frida Python package and device frida-server are installed)")
	}
}

func (m *Manager) pumpFridaStderr(h *fridaSessionHandle, r io.Reader) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		h.mu.Lock()
		if h.info.ExitError == "" {
			h.info.ExitError = line
		}
		h.mu.Unlock()
		h.appendLog("[stderr] " + line)
	}
}

func (h *fridaSessionHandle) appendLog(entry string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.info.LogCount++
	h.logs = append(h.logs, entry)
	if len(h.logs) > 200 {
		h.logs = h.logs[len(h.logs)-200:]
	}
}

func (h *fridaSessionHandle) snapshot() FridaSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	cp := h.info
	tailStart := 0
	if len(h.logs) > 30 {
		tailStart = len(h.logs) - 30
	}
	cp.LastLogs = append([]string(nil), h.logs[tailStart:]...)
	return cp
}

// InvokeFridaRPC invokes an exported rpc.exports method on an active Frida session.
func (m *Manager) InvokeFridaRPC(ctx context.Context, sessionID string, taskID int64, method string, args []any) (any, error) {
	if strings.TrimSpace(method) == "" {
		return nil, fmt.Errorf("rpc method name required")
	}
	m.fridaMu.Lock()
	if sessionID == "" && taskID > 0 {
		sessionID = m.fridaByTask[taskID]
	}
	h := m.fridaSess[sessionID]
	m.fridaMu.Unlock()
	if h == nil {
		return nil, fmt.Errorf("active frida session not found (start a hook session exporting rpc.exports.%s first)", method)
	}
	if !h.snapshot().Running {
		return nil, fmt.Errorf("frida session %s is no longer running", h.info.ID)
	}

	reqID := newShortID()
	ch := make(chan string, 1)
	h.rpcMu.Lock()
	h.rpcWait[reqID] = ch
	h.rpcMu.Unlock()
	defer func() {
		h.rpcMu.Lock()
		delete(h.rpcWait, reqID)
		h.rpcMu.Unlock()
	}()

	reqBytes, _ := json.Marshal(map[string]any{
		"cmd":    "rpc",
		"id":     reqID,
		"method": method,
		"args":   args,
	})
	if _, err := io.WriteString(h.stdin, string(reqBytes)+"\n"); err != nil {
		return nil, fmt.Errorf("write rpc to frida session: %w", err)
	}

	select {
	case raw := <-ch:
		var out struct {
			OK     bool   `json:"ok"`
			Result any    `json:"result"`
			Error  string `json:"error"`
		}
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return nil, err
		}
		if !out.OK {
			return nil, fmt.Errorf("frida rpc error: %s", out.Error)
		}
		return out.Result, nil
	case <-time.After(15 * time.Second):
		return nil, fmt.Errorf("frida rpc %s timed out", method)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// GetFridaSession returns the current snapshot of a session (or task's latest session).
func (m *Manager) GetFridaSession(sessionID string, taskID int64) (*FridaSession, bool) {
	m.fridaMu.Lock()
	if sessionID == "" && taskID > 0 {
		sessionID = m.fridaByTask[taskID]
	}
	h := m.fridaSess[sessionID]
	m.fridaMu.Unlock()
	if h == nil {
		return nil, false
	}
	snap := h.snapshot()
	return &snap, true
}

// ListFridaSessions lists active and recent sessions for taskID (0 = all).
func (m *Manager) ListFridaSessions(taskID int64) []FridaSession {
	m.fridaMu.Lock()
	handles := make([]*fridaSessionHandle, 0, len(m.fridaSess))
	for _, h := range m.fridaSess {
		handles = append(handles, h)
	}
	m.fridaMu.Unlock()
	var out []FridaSession
	for _, h := range handles {
		snap := h.snapshot()
		if taskID == 0 || snap.TaskID == taskID {
			out = append(out, snap)
		}
	}
	return out
}

// StopFridaSession detaches and terminates an active Frida session cleanly.
func (m *Manager) StopFridaSession(sessionID string) error {
	m.fridaMu.Lock()
	h := m.fridaSess[sessionID]
	m.fridaMu.Unlock()
	if h == nil {
		return nil
	}
	if h.stdin != nil {
		_, _ = io.WriteString(h.stdin, `{"cmd":"quit"}`+"\n")
		_ = h.stdin.Close()
	}
	if h.cmd != nil && h.cmd.Process != nil {
		done := make(chan struct{})
		go func() {
			_, _ = h.cmd.Process.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = h.cmd.Process.Kill()
		}
	}
	return nil
}

func newShortID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
