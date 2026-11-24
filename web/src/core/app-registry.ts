// The only file of src/core that imports src/app (checked by
// src/core/__tests__/architecture.test.ts): the app's registration, which the shell reads.
import { app } from "../app/registry";
import type { AppRegistration } from "./app-types";

export const registeredApp: AppRegistration = app;
