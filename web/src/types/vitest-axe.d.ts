// Wave 4 PR-A: 让 vitest 4 识别 vitest-axe 的 toHaveNoViolations matcher。
// 上游包只 augment 了 Vi global namespace，但 vitest 4 用 @vitest/expect#Matchers，
// 因此在这里手动做一次模块扩展。
import "vitest";

declare module "@vitest/expect" {
  interface Matchers<T = unknown> {
    toHaveNoViolations(): T;
  }
}

export {};
