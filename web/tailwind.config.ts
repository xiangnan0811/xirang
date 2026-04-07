import type { Config } from "tailwindcss";

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
      borderRadius: {
        lg: "var(--radius-lg)",
        md: "var(--radius)",
        sm: "var(--radius-sm)"
      },
      boxShadow: {
        sm: "var(--shadow-sm)",
        md: "var(--shadow-md)",
        lg: "var(--shadow-lg)"
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
  plugins: []
} satisfies Config;
