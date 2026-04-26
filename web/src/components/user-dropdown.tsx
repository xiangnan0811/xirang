import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { ChevronDown, LogOut, Settings } from "lucide-react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Button } from "@/components/ui/button";
import { useAuth } from "@/context/auth-context";
import { apiClient } from "@/lib/api/client";

export function UserDropdown() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { username, role, token, logout } = useAuth();

  const handleLogout = async () => {
    if (token) {
      try {
        await apiClient.logout(token);
      } catch {
        // 即便服务端注销失败，也执行本地会话清理
      }
    }
    logout();
    navigate("/login", { replace: true });
  };

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="sm"
          className="h-8 gap-1 px-2 text-xs text-muted-foreground hover:text-foreground"
        >
          {username ?? t("common.unknown")}
          <ChevronDown className="size-3" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-48">
        <DropdownMenuLabel>
          {username ?? t("common.unknown")}
          <span className="ml-1.5 text-micro font-normal text-muted-foreground">
            {role}
          </span>
        </DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuItem onClick={() => navigate("/app/settings")}>
          <Settings className="mr-2 size-4" />
          {t("nav.settings")}
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem onClick={() => void handleLogout()}>
          <LogOut className="mr-2 size-4" />
          {t("appShell.logout")}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
