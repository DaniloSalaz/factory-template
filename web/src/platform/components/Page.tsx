import type { ReactNode } from "react";

/** Page is the heading of a screen and its content. */
export function Page(props: { title: string; description?: string; children: ReactNode }) {
  return (
    <section className="mx-auto flex w-full max-w-3xl flex-col gap-6">
      <header className="flex flex-col gap-1">
        <h1 className="font-heading text-2xl font-semibold">{props.title}</h1>
        {props.description && <p className="text-muted-foreground">{props.description}</p>}
      </header>
      {props.children}
    </section>
  );
}
