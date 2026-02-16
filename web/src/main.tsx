import React from "react";
import ReactDOM from "react-dom/client";
import { RouterProvider } from "react-router-dom";
import { AppRouter } from "./router";
import { ErrorBoundary } from "./components/error-boundary";
import { Toaster } from "./components/ui/toast";
import { ThemeProvider } from "./context/theme-context";
import { AuthProvider } from "./context/auth-context";
import "./index.css";

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <ThemeProvider>
      <AuthProvider>
        <ErrorBoundary>
          <RouterProvider router={AppRouter} />
        </ErrorBoundary>
        <Toaster />
      </AuthProvider>
    </ThemeProvider>
  </React.StrictMode>
);
