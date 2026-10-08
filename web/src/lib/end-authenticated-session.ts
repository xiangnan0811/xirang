export async function endAuthenticatedSession(input: {
  token: string | null;
  logout: () => void;
  requestLogout: (token: string) => Promise<unknown>;
}): Promise<void> {
  const capturedToken = input.token;
  input.logout();
  if (!capturedToken) return;
  try {
    await input.requestLogout(capturedToken);
  } catch {
    // Local session is already cleared. A failed server logout must not restore it.
  }
}
