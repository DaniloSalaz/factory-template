// Every text of the platform's UI, in Spanish. There is no i18n yet: when it comes, this
// module is what it replaces.

export const texts = {
  common: {
    loading: "Cargando…",
    retry: "Reintentar",
    cancel: "Cancelar",
    dismiss: "Cerrar aviso",
    unexpected: "Algo ha fallado. Inténtalo de nuevo.",
    requestId: (id: string) => `Referencia: ${id}`,
  },
  shell: {
    tenants: "Organizaciones",
    security: "Seguridad",
    logout: "Salir",
    logoutAll: "Cerrar todas las sesiones",
    logoutFailed: "No se ha podido cerrar la sesión. Inténtalo de nuevo.",
    mainNav: "Navegación principal",
    tenantNav: "Navegación de la organización",
    overview: "Resumen",
  },
  login: {
    title: "Entrar",
    description: "Entra con tu cuenta para continuar.",
    button: "Entrar",
  },
  tenants: {
    title: "Organizaciones",
    description: "Elige en qué organización quieres trabajar.",
    empty: "Todavía no perteneces a ninguna organización. Crea una para empezar.",
    suspended: "Suspendida",
    role: (role: string) => `Rol: ${role}`,
    mfaRequired: "Exige verificación en dos pasos",
    forbidden: "No tienes acceso a esa organización o no está disponible. Elige otra de la lista.",
    createTitle: "Crear una organización",
    createDescription: "Serás su propietario. El identificador no se puede cambiar después.",
    name: "Nombre",
    slug: "Identificador",
    slugHint: "De 3 a 40 minúsculas, números y guiones, sin guion al principio ni al final.",
    create: "Crear",
    creating: "Creando…",
  },
  tenant: {
    title: "Organización",
    slug: "Identificador",
    role: "Tu rol",
    scope: "Alcance",
    scopes: { tenant: "Toda la organización", own: "Solo lo tuyo" },
    permissions: "Permisos",
    noPermissions: "Tu rol no tiene permisos en esta organización.",
    permissionsNote: "Lo que ves depende de tus permisos; el servidor los comprueba siempre.",
  },
  mfa: {
    title: "Verificación en dos pasos",
    description:
      "Protege tu cuenta con un código de una app de autenticación (Google Authenticator, 1Password…).",
    factors: "Tus factores",
    noFactors: "No tienes ningún factor dado de alta.",
    factorTotp: "App de autenticación",
    verified: "Verificado",
    pending: "Pendiente de verificar",
    createdAt: (date: string) => `Alta: ${date}`,
    sessionVerifiedAt: (date: string) => `Esta sesión verificó un código el ${date}.`,
    sessionNotVerified: "Esta sesión todavía no ha verificado ningún código.",
    add: "Añadir un factor",
    verifyNow: "Verificar ahora",
    remove: "Quitar",
    confirmRemove: "¿Quitar este factor?",
    confirmRemoveButton: "Sí, quitar",
    verifyTitle: "Introduce tu código",
    verifyDescription:
      "Tu rol en esta organización exige la verificación en dos pasos. Escribe el código de 6 dígitos de tu app.",
    verifyStandaloneDescription: "Escribe el código de 6 dígitos de tu app de autenticación.",
    noVerifiedFactor: "No tienes ningún factor verificado. Da uno de alta para continuar.",
    chooseFactor: "Factor",
    factorLabel: (n: number) => `App de autenticación ${n}`,
    code: "Código",
    verify: "Verificar",
    verifying: "Verificando…",
    enrollTitle: "Dar de alta la verificación en dos pasos",
    enrollRequired:
      "Tu rol en esta organización exige la verificación en dos pasos y aún no la tienes. Dala de alta para continuar.",
    enrollStart: "Empezar el alta",
    enrollStarting: "Preparando…",
    enrollScan: "Escanea este código QR con tu app de autenticación.",
    enrollQrAlt: "Código QR para la app de autenticación",
    enrollSecret: "Si tu app no puede escanearlo, introduce esta clave:",
    enrollOnce: "Solo se muestra ahora. No la guardes en ningún otro sitio.",
    enrollCode: "Después escribe el código de 6 dígitos que muestra la app.",
    enrolled: "Factor dado de alta.",
  },
  notices: {
    rateLimited: (seconds?: number) =>
      seconds === undefined
        ? "Demasiados intentos. Espera un poco y vuelve a intentarlo."
        : `Demasiados intentos. Vuelve a intentarlo dentro de ${formatWait(seconds)}.`,
  },
  /** Messages for the API's error codes that a screen shows next to its form. */
  errors: {
    "tenant.slug_invalid":
      "El identificador debe tener de 3 a 40 minúsculas, números o guiones, sin guion al principio ni al final.",
    "tenant.slug_reserved": "Ese identificador está reservado. Elige otro.",
    "tenant.slug_taken": "Ese identificador ya está en uso. Elige otro.",
    "tenant.name_invalid": "El nombre debe tener entre 1 y 100 caracteres.",
    "tenant.limit_reached": "Has creado tantas organizaciones como permite tu cuenta.",
    "mfa.code_invalid": "El código no es correcto. Vuelve a intentarlo con el código actual.",
    "mfa.code_reused": "Ese código ya se ha usado. Espera al siguiente.",
    "mfa.challenge_expired": "Ha pasado demasiado tiempo. Vuelve a intentarlo.",
    "mfa.challenge_used": "Vuelve a intentarlo con el código actual.",
    "mfa.challenge_not_found": "Vuelve a intentarlo con el código actual.",
    "mfa.factor_not_found": "Ese factor ya no existe.",
    "mfa.too_many_pending": "Termina o quita tus altas pendientes antes de empezar otra.",
    "mfa.last_factor_required":
      "No puedes quitar tu último factor: uno de tus roles exige la verificación en dos pasos.",
    "mfa.provider_rejected":
      "El proveedor de identidad ha rechazado la operación. Empieza de nuevo.",
    "rate_limit.unavailable": "El servicio no está disponible ahora. Inténtalo más tarde.",
    validation: "Revisa los datos del formulario.",
  } as Record<string, string>,
} as const;

function formatWait(seconds: number): string {
  if (seconds < 60) {
    return seconds === 1 ? "1 segundo" : `${seconds} segundos`;
  }
  const minutes = Math.ceil(seconds / 60);
  return minutes === 1 ? "1 minuto" : `${minutes} minutos`;
}

export function formatDateTime(iso: string): string {
  return new Intl.DateTimeFormat("es", { dateStyle: "medium", timeStyle: "short" }).format(
    new Date(iso),
  );
}
