const TOKEN_KEY = "blade_token";
const COOKIE_MAX_AGE = 7 * 24 * 60 * 60; // 7 天（秒）

export interface CurrentUser {
  id: string;
  name: string;
  username: string;
  email: string;
  avatar: string;
  role: string;
}

function readCookie(name: string): string | null {
  if (typeof document === "undefined") return null;
  const prefix = `${name}=`;
  for (const part of document.cookie.split(";")) {
    const item = part.trim();
    if (item.startsWith(prefix)) return decodeURIComponent(item.slice(prefix.length));
  }
  return null;
}

export const auth = {
  getToken(): string | null {
    if (typeof window === "undefined") return null;
    // Mock demo：无真实登录，返回一个假 token 让路由守卫放行、直接进主界面。
    return localStorage.getItem(TOKEN_KEY) ?? (process.env.NEXT_PUBLIC_MOCK === "1" ? "mock-demo" : null);
  },

  // 登录后 localStorage 与 cookie 可能不同步：旧版本只写 localStorage、cookie 过期、
  // 或用户在 localhost / 127.0.0.1 / 反代域名之间切换，都会让 fetch（Authorization 头）
  // 正常而 EventSource（只能靠 cookie 或查询串）静默 401。挂载时把当前 origin 的
  // cookie 重新对齐到 localStorage 里的有效 token。
  ensureCookie(): void {
    const token = this.getToken();
    if (!token || readCookie(TOKEN_KEY) === token) return;
    this.setToken(token);
  },

  setToken(token: string): void {
    localStorage.setItem(TOKEN_KEY, token);
    // 同步写 cookie，供 Next.js middleware 服务端读取。
    // HTTPS 下带 Secure：否则一次明文 HTTP 请求就可能把它带出去（本地跑 HTTP 时不加，
    // 加了 cookie 反而会失效）。
    // 这里做不到 HttpOnly——cookie 由 JS 写，而且 token 本来就存在 localStorage 里；
    // 要真正挡住 XSS 读凭据，得整体改成服务端 Set-Cookie 的 HttpOnly 方案，属架构级改动。
    const secure = location.protocol === "https:" ? "; Secure" : "";
    document.cookie = `${TOKEN_KEY}=${encodeURIComponent(token)}; path=/; max-age=${COOKIE_MAX_AGE}; SameSite=Lax${secure}`;
  },

  clearToken(): void {
    localStorage.removeItem(TOKEN_KEY);
    document.cookie = `${TOKEN_KEY}=; path=/; max-age=0`;
  },

  // 从 JWT payload 的 sub 字段解析当前用户，仅用于展示，不做签名验证。
  getCurrentUser(): CurrentUser | null {
    const token = this.getToken();
    if (!token) return null;
    try {
      const parts = token.split(".");
      if (parts.length !== 3) return null;
      // base64url → base64
      const payload = JSON.parse(atob(parts[1].replace(/-/g, "+").replace(/_/g, "/")));
      const username: string = payload.sub ?? "操作员";
      return { id: "1", name: username, username, email: "", avatar: "", role: "operator" };
    } catch {
      return null;
    }
  },
};
