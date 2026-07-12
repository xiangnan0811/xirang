import React from "react";
import ReactDOM from "react-dom/client";
import { RouterProvider } from "react-router-dom";
import { AppRouter } from "./router";
import { ErrorBoundary } from "./components/error-boundary";
import { Toaster } from "./components/ui/toast";
import { MotionPreferenceBoundary } from "./components/motion-preference-boundary";
import { ThemeProvider } from "./context/theme-context";
import { AuthProvider } from "./context/auth-context";
import { i18nReady } from "./i18n";
import "@fontsource-variable/inter";
import "./index.css";

i18nReady.then(() => {
  ReactDOM.createRoot(document.getElementById("root")!).render(
    <React.StrictMode>
      <ThemeProvider>
        <MotionPreferenceBoundary>
          <AuthProvider>
            <ErrorBoundary>
              <RouterProvider router={AppRouter} />
            </ErrorBoundary>
            <Toaster />
          </AuthProvider>
        </MotionPreferenceBoundary>
      </ThemeProvider>
    </React.StrictMode>
  );
});


if (import.meta.env.PROD && "serviceWorker" in navigator) {
  window.addEventListener("load", () => {
    navigator.serviceWorker.register("/sw.js").catch(() => undefined);
  });
  // After a new SW takes control, reload once so clients pick up the new bundle
  // (static assets are cache-first; navigate alone does not replace in-memory modules).
  let refreshing = false;
  navigator.serviceWorker.addEventListener("controllerchange", () => {
    if (refreshing) return;
    refreshing = true;
    window.location.reload();
  });
}
