// The only file of src/platform that imports src/app (checked by
// src/platform/__tests__/architecture.test.ts): the app's registration, which the shell reads.
import { app } from "../app/registry";
import type { AppRegistration } from "./app-types";

export const registeredApp: AppRegistration = app;
