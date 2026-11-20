import { useState } from "react";
import { apiUrl } from "./api/fetcher";
import { authLogout, getAuthLoginUrl } from "./gen/api";

// Only enough to try the login by hand; the real front end is step 8 of the plan.
export default function App() {
  const [error, setError] = useState<string | null>(null);

  function login() {
    window.location.assign(apiUrl(getAuthLoginUrl({ return_to: "/" })));
  }

  async function logout() {
    setError(null);
    const res = await authLogout();
    if (res.status !== 200) {
      setError(`Logout failed (${res.status}).`);
      return;
    }
    // Ends the AuthKit session too; it comes back to the dashboard's Sign-out URI.
    if (res.data.logout_url) {
      window.location.assign(res.data.logout_url);
      return;
    }
    window.location.reload();
  }

  return (
    <main>
      <h1>Factory Template</h1>
      <button type="button" onClick={login}>
        Entrar
      </button>{" "}
      <button type="button" onClick={logout}>
        Salir
      </button>
      {error && <p role="alert">{error}</p>}
    </main>
  );
}
