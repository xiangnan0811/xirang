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
  // 阶段策略：PR-A 全部高频规则降级为 warn 暴露债务；PR-D 升回 error。
  // 仅作用于源码 JSX/TSX，避免污染脚本与服务工作者代码。
  {
    files: ["src/**/*.{ts,tsx,js,jsx}"],
    plugins: {
      "jsx-a11y": jsxA11y,
    },
    rules: {
      ...jsxA11y.flatConfigs.recommended.rules,
      // 历史债务降级（PR-A 阶段不卡 CI；PR-D 升回 error）
      "jsx-a11y/label-has-associated-control": "warn",
      "jsx-a11y/no-noninteractive-tabindex": "warn",
      "jsx-a11y/click-events-have-key-events": "warn",
      "jsx-a11y/no-static-element-interactions": "warn",
      "jsx-a11y/no-autofocus": "warn",
      "jsx-a11y/anchor-is-valid": "warn",
      // recommended 默认 error 的规则也先降级，由 PR-B/PR-D 修完再升回 error
      "jsx-a11y/aria-role": "warn",
      "jsx-a11y/no-redundant-roles": "warn",
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
