export function appLayoutKey(pathname: string): string {
  const segments = pathname.split("/").filter(Boolean);
  if (segments[0] === "app" && segments[1]) {
    return `/app/${segments[1]}`;
  }
  return pathname || "/";
}
