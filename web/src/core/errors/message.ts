import { ApiError } from "@/core/api/problem";
import { texts } from "@/core/texts";
import { isHandledGlobally } from "./handle";

/**
 * errorMessage is the text a screen shows for a failed request, or null when there is
 * nothing to show there: no error, or one the platform already handles (it navigates or
 * shows a notice). Screens never decide by status themselves.
 */
export function errorMessage(error: unknown): string | null {
  if (!error || isHandledGlobally(error)) {
    return null;
  }
  if (error instanceof ApiError) {
    const known = error.code ? texts.errors[error.code] : undefined;
    if (known) {
      return known;
    }
    if (error.status === 400 || error.status === 422) {
      return texts.errors["validation"] ?? texts.common.unexpected;
    }
  }
  return texts.common.unexpected;
}

/** errorCode is the API's code of the error, for screens that place it by field. */
export function errorCode(error: unknown): string | undefined {
  return error instanceof ApiError ? error.code : undefined;
}

/** requestIdOf is the request ID of an API error, to quote it when reporting a fault. */
export function requestIdOf(error: unknown): string | undefined {
  return error instanceof ApiError ? error.requestId : undefined;
}
