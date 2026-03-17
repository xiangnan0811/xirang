import { Component, type ErrorInfo, type ReactNode } from "react";
import i18next from "i18next";
import { AlertTriangle } from "lucide-react";
import { Button } from "@/components/ui/button";

interface ErrorBoundaryProps {
  children: ReactNode;
  fallback?: ReactNode;
}

interface ErrorBoundaryState {
  hasError: boolean;
  error: Error | null;
}

class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  constructor(props: ErrorBoundaryProps) {
    super(props);
    this.state = { hasError: false, error: null };
  }

  static getDerivedStateFromError(error: Error): ErrorBoundaryState {
    return { hasError: true, error };
  }

  componentDidCatch(error: Error, errorInfo: ErrorInfo) {
    // ErrorBoundary 是合理的 console.error 使用场景，用于记录未捕获的渲染错误
    console.error("ErrorBoundary caught:", error, errorInfo);
  }

  private handleReset = () => {
    this.setState({ hasError: false, error: null });
  };

  render() {
    if (this.state.hasError) {
      if (this.props.fallback) {
        return this.props.fallback;
      }

      return (
        <div className="flex flex-col items-center justify-center py-16 text-center">
          <div className="mb-4 rounded-full bg-destructive/10 p-3">
            <AlertTriangle className="size-6 text-destructive" />
          </div>
          <h3 className="text-sm font-semibold">{i18next.t('errorBoundary.renderError')}</h3>
          <p className="mt-1 max-w-md text-sm text-muted-foreground">
            {this.state.error?.message ?? i18next.t('errorBoundary.unknownError')}
          </p>
          <Button
            variant="outline"
            size="sm"
            className="mt-4"
            onClick={this.handleReset}
          >
            {i18next.t('common.retry')}
          </Button>
        </div>
      );
    }

    return this.props.children;
  }
}

export { ErrorBoundary };
