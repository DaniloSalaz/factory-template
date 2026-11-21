// The app's own texts, in Spanish (the platform's are in src/platform/texts.ts).
export const appTexts = {
  exampleNav: "Ejemplo",
  exampleTitle: "Pantalla de ejemplo",
  exampleDescription: (tenant: string) =>
    `Esta pantalla es de la app, no de la plataforma. Estás en ${tenant}.`,
  exampleAction: "Renombrar la organización",
  exampleActionNote:
    "Solo la ves si tu rol tiene tenant:update; el servidor lo vuelve a comprobar.",
} as const;
