// 生产构建的替身。见 next.config.mjs 的 turbopack.resolveAlias：非 mock 构建下
// `@/lib/mock/handler` 与 `@/lib/mock/report` 都解析到这里，于是那 6.7k 行演示数据
// （含示例目标与示例口令）根本不会进产物。
//
// 这两个函数在正常构建里永远不会被调用——调用点都在 `if (MOCK)` 里，而 MOCK 是构建期
// 常量 false。留着实现只是为了类型对得上。

export async function mockHandle<T>(_method: string, _rawPath: string, _body?: BodyInit | null): Promise<T> {
  throw new Error("mock 层在本构建中不可用（NEXT_PUBLIC_MOCK != 1）");
}

export function mockReport(_task?: string): string {
  throw new Error("mock 层在本构建中不可用（NEXT_PUBLIC_MOCK != 1）");
}
