// Fails early, and says what to start, when the API or the database is not there.
import { sql } from "./db.ts";

export default async function globalSetup() {
  try {
    const res = await fetch("http://localhost:8080/readyz");
    if (!res.ok) throw new Error(`readyz answered ${res.status}`);
  } catch (err) {
    throw new Error(
      `The API is not ready on :8080 (${String(err)}). Run \`mise run up && mise run migrate\` and \`mise run dev\`.`,
    );
  }
  sql("SELECT 1");
}
