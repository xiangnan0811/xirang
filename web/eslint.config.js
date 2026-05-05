import js from "@eslint/js";
import tseslint from "typescript-eslint";
import reactHooks from "eslint-plugin-react-hooks";
import reactRefresh from "eslint-plugin-react-refresh";
import jsxA11y from "eslint-plugin-jsx-a11y";
import globals from "globals";

export default tseslint.config(
  { ignores: ["dist/", "coverage/"] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    plugins: {
      "react-hooks": reactHooks,
      "react-refresh": reactRefresh,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      "react-refresh/only-export-components": [
        "warn",
        { allowConstantExport: true },
      ],
      // Allow _-prefixed variables to be unused (common convention for intentionally unused params)
      "@typescript-eslint/no-unused-vars": [
        "error",
        { argsIgnorePattern: "^_", varsIgnorePattern: "^_" },
      ],
    },
  },
  // Wave 4 PR-A: a11y 静态检查（jsx-a11y）。
  // 阶段策略：PR-A 全部高频规则降级为 warn 暴露债务；PR-B/C 修真违规；PR-D 升回 error。
  // 仅作用于源码 JSX/TSX，避免污染脚本与服务工作者代码。
  {
    files: ["src/**/*.{ts,tsx,js,jsx}"],
    plugins: {
      "jsx-a11y": jsxA11y,
    },
    rules: {
      ...jsxA11y.flatConfigs.recommended.rules,
      // PR-D 阶段：以下规则当前已 0 违规，升回 error 锁定基线（未来回归直接 CI 拒）。
      "jsx-a11y/aria-role": "error",
      "jsx-a11y/no-redundant-roles": "error",
      "jsx-a11y/anchor-is-valid": "error",
      // PR-D 阶段保持 warn：仍存在历史债务，预算外不在本 wave 修。
      // 后续 wave 计划：
      //   - label-has-associated-control: tasks-page.dialogs.tsx 3 处 + rotation-preview 1 处
      //     需重构 label/input 关联（非纯 attribute 增补）。
      //   - no-autofocus: 4 处（login / totp setup/disable / command-palette）
      //     UX 上有意保留焦点跳转；改造前需评估对键盘用户的影响。
      //   - click-events-have-key-events / no-static-element-interactions:
      //     dashboards-page + nodes-page.grid.tsx，需替换为 button 或加键盘 handler。
      //   - no-noninteractive-tabindex: nodes-page.grid.tsx 1 处。
      "jsx-a11y/label-has-associated-control": "warn",
      "jsx-a11y/no-noninteractive-tabindex": "warn",
      "jsx-a11y/click-events-have-key-events": "warn",
      "jsx-a11y/no-static-element-interactions": "warn",
      "jsx-a11y/no-autofocus": "warn",
    },
  },
  // CommonJS config files use module/require
  {
    files: ["**/*.cjs"],
    languageOptions: {
      globals: globals.commonjs,
    },
  },
  // Service worker runs in a serviceworker global context
  {
    files: ["public/sw.js"],
    languageOptions: {
      globals: globals.serviceworker,
    },
  },
  // Node.js scripts
  {
    files: ["scripts/**/*.mjs", "scripts/**/*.js"],
    languageOptions: {
      globals: globals.node,
    },
  }
);
