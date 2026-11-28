"use client";

import { useCallback, useEffect, useState } from "react";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { http } from "@/lib/api";

type Cert = { mode: string; host: string; san: string[]; expires: string; fingerprint: string; ca?: string };
type Config = { serving: boolean; active?: Cert; candidate?: Cert; active_error?: string; candidate_error?: string };

export function TLSCard() {
  const [state, setState] = useState<Config>();
  const [host, setHost] = useState("");
  const [cert, setCert] = useState<File>();
  const [key, setKey] = useState<File>();
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const reload = useCallback(async () => setState(await http<Config>("/system/tls")), []);
  useEffect(() => {
    setHost(window.location.hostname.replace(/^\[|\]$/g, ""));
    void reload().catch((e) => setMessage(String(e)));
  }, [reload]);
  const secure =
    typeof window !== "undefined" &&
    (location.protocol === "https:" || ["localhost", "127.0.0.1", "[::1]"].includes(location.hostname));
  async function change(action: string) {
    setBusy(true);
    setMessage("");
    try {
      const body: Record<string, string> = { action, host };
      if (action === "import") {
        if (!secure || !cert || !key) throw Error("请通过本机地址或 HTTPS 选择证书及私钥");
        body.certificate = await cert.text();
        body.key = await key.text();
      }
      if (action === "enable" && state?.candidate) body.fingerprint = state.candidate.fingerprint;
      await http("/system/tls", { method: "POST", body: JSON.stringify(body) });
      await reload();
      setMessage(action === "enable" || action === "disable" ? "已保存，下次重启生效。" : "候选证书已准备好。");
    } catch (e) {
      setMessage(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Card>
      <CardHeader>
        <CardTitle>控制台 HTTPS</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3 text-sm">
        <p>
          控制台监听 {state ? (state.serving ? "HTTPS" : "HTTP") : "…"} · 重启后 {state?.active ? "HTTPS" : "HTTP"}
        </p>
        <label className="grid gap-1">
          浏览器访问的域名或 IP
          <Input value={host} onChange={(e) => setHost(e.target.value)} />
        </label>
        <Button disabled={busy || !host} onClick={() => change("generate")}>
          生成本地 CA 和证书
        </Button>
        {secure ? (
          <div className="flex flex-wrap items-end gap-2">
            <label>
              证书链 PEM
              <Input type="file" onChange={(e) => setCert(e.target.files?.[0])} />
            </label>
            <label>
              私钥 PEM
              <Input type="file" onChange={(e) => setKey(e.target.files?.[0])} />
            </label>
            <Button variant="outline" disabled={busy || !cert || !key} onClick={() => change("import")}>
              导入证书
            </Button>
          </div>
        ) : (
          <p>
            导入私钥请使用本机回环地址或 HTTPS，也可在服务器执行{" "}
            <code>./blade -console-cert chain.pem -console-key key.pem -console-host {host}</code>。
          </p>
        )}
        {(["active", "candidate"] as const).map((k) => {
          const c = state?.[k];
          return (
            c && (
              <div key={k} className="rounded border p-2">
                <strong>{k === "active" ? "重启配置" : "候选证书"}</strong>
                <p>
                  {c.san.join(", ")} · 到期 {new Date(c.expires).toLocaleString()}
                </p>
                {state?.[k === "active" ? "active_error" : "candidate_error"]}
                <p>
                  {c.ca && (
                    <a
                      className="underline"
                      href={`data:application/x-pem-file;charset=utf-8,${encodeURIComponent(c.ca)}`}
                      download="blade-console-ca.pem"
                    >
                      下载并信任本地 CA
                    </a>
                  )}
                </p>
              </div>
            )
          );
        })}
        <div className="flex gap-2">
          <Button disabled={busy || !state?.candidate || !!state.candidate_error} onClick={() => change("enable")}>
            启用候选证书
          </Button>
          <Button variant="outline" disabled={busy || !state?.active} onClick={() => change("disable")}>
            切换回 HTTP
          </Button>
        </div>
        {message && <p role="status">{message}</p>}
      </CardContent>
    </Card>
  );
}
