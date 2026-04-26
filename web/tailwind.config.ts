import type { Config } from "tailwindcss";
import tailwindcssAnimate from "tailwindcss-animate";

export default {
  darkMode: ["class"],
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        background: "hsl(var(--background))",
        foreground: "hsl(var(--foreground))",
        card: { DEFAULT: "hsl(var(--card))", foreground: "hsl(var(--card-foreground))" },
        popover: { DEFAULT: "hsl(var(--popover))", foreground: "hsl(var(--popover-foreground))" },
        primary: { DEFAULT: "hsl(var(--primary))", foreground: "hsl(var(--primary-foreground))" },
        secondary: { DEFAULT: "hsl(var(--secondary))", foreground: "hsl(var(--secondary-foreground))" },
        muted: { DEFAULT: "hsl(var(--muted))", foreground: "hsl(var(--muted-foreground))" },
        accent: { DEFAULT: "hsl(var(--accent))", foreground: "hsl(var(--accent-foreground))" },
        destructive: { DEFAULT: "hsl(var(--destructive))", foreground: "hsl(var(--destructive-foreground))" },
        success: { DEFAULT: "hsl(var(--success))", foreground: "hsl(var(--success-foreground))" },
        warning: { DEFAULT: "hsl(var(--warning))", foreground: "hsl(var(--warning-foreground))" },
        info: { DEFAULT: "hsl(var(--info))", foreground: "hsl(var(--info-foreground))" },
        border: "hsl(var(--border))",
        input: "hsl(var(--input))",
        ring: "hsl(var(--ring))",
        "accent-brand": "hsl(var(--accent-brand))",
        "accent-brand-foreground": "hsl(var(--background))",
        chart: {
          1: "hsl(var(--chart-1))",
          2: "hsl(var(--chart-2))",
          3: "hsl(var(--chart-3))",
          ingress: "hsl(var(--chart-ingress))",
          egress: "hsl(var(--chart-egress))"
        }
      },
      fontFamily: {
        sans: ['"Inter Variable"', '"Inter"', '"PingFang SC"', '"Microsoft YaHei"', "system-ui", "sans-serif"],
        mono: ['"JetBrains Mono"', "monospace"]
      },
      // Project-specific size tokens. These names sit alongside Tailwind's
      // built-in xs/sm/base/... scale rather than overriding it. Each was
      // promoted from a recurring `text-[Npx]` arbitrary value (audit ran
      // 2026-04-25). If you need a one-off size that doesn't fit, prefer
      // `text-[Npx]` over forcing it into a token name that doesn't match.
      fontSize: {
        // 10px — micro labels: badges, source tags, tiny chips (~22 sites)
        micro: ["10px", { lineHeight: "14px" }],
        // 11px — small meta text: stat captions, table headers, footnotes (~17 sites)
        mini: ["11px", { lineHeight: "16px" }],
        // 13px — sidebar nav items (3 sites)
        nav: ["13px", { lineHeight: "18px" }],
        // 28px — hero stat numbers on overview cards (2 sites)
        stat: ["28px", { lineHeight: "32px" }],
      },
      borderRadius: {
        // 4px — small status dots, micro icon containers
        xs: "0.25rem",
        sm: "var(--radius-sm)",
        DEFAULT: "var(--radius)",
        md: "var(--radius)",
        lg: "var(--radius-lg)",
        xl: "var(--radius-xl)",
      },
      boxShadow: {
        sm: "var(--shadow-sm)",
        DEFAULT: "var(--shadow-md)",
        md: "var(--shadow-md)",
        lg: "var(--shadow-lg)",
        xl: "var(--shadow-xl)",
      },
      animation: {
        "fade-in": "fade-in var(--duration-slow) var(--ease-enter)",
        "slide-up": "slide-up var(--duration-slow) var(--ease-enter)",
        "slide-down": "slide-down var(--duration-slow) var(--ease-enter)",
        "animate-in": "animate-in var(--duration-slow) var(--ease-enter)",
        "popover-in": "popover-in var(--duration-fast) var(--ease-enter)",
        "popover-out": "popover-out var(--duration-fast) var(--ease-exit)"
      },
      keyframes: {
        "fade-in": { from: { opacity: "0" }, to: { opacity: "1" } },
        "slide-up": { from: { opacity: "0", translate: "0 8px" }, to: { opacity: "1", translate: "0 0" } },
        "slide-down": { from: { opacity: "0", translate: "0 -8px" }, to: { opacity: "1", translate: "0 0" } },
        "animate-in": { from: { opacity: "0", scale: "0.96" }, to: { opacity: "1", scale: "1" } },
        "popover-in": { from: { opacity: "0", scale: "0.94" }, to: { opacity: "1", scale: "1" } },
        "popover-out": { from: { opacity: "1", scale: "1" }, to: { opacity: "0", scale: "0.94" } }
      }
    }
  },
  plugins: [tailwindcssAnimate]
} satisfies Config;
