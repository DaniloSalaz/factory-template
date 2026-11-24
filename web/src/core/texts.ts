// Every text of the core's UI, in Spanish. There is no i18n yet: when it comes, this
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
    members: "Miembros",
    invitations: "Invitaciones",
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
    renameTitle: "Cambiar el nombre",
    name: "Nombre",
    rename: "Guardar",
    renaming: "Guardando…",
    renamed: "Nombre cambiado.",
  },
  members: {
    title: "Miembros",
    description: "Las personas de esta organización y su rol.",
    empty: "No hay miembros que mostrar.",
    loadMore: "Cargar más",
    you: "Tú",
    joinedAt: (date: string) => `Desde el ${date}`,
    role: "Rol",
    roleFor: (who: string) => `Rol de ${who}`,
    changeRole: "Cambiar rol",
    changing: "Cambiando…",
    remove: "Expulsar",
    confirmRemove: (who: string) => `¿Expulsar a ${who} de la organización?`,
    confirmRemoveButton: "Sí, expulsar",
    leave: "Salir de la organización",
    confirmLeave:
      "¿Salir de esta organización? Perderás el acceso hasta que alguien vuelva a invitarte.",
    confirmLeaveButton: "Sí, salir",
    left: "Has salido de la organización.",
  },
  invitations: {
    title: "Invitaciones",
    description: "Las invitaciones pendientes y caducadas. Una caducada se puede reenviar.",
    empty: "No hay invitaciones pendientes.",
    loadMore: "Cargar más",
    expired: "Caducada",
    expiresAt: (date: string) => `Caduca el ${date}`,
    role: (role: string) => `Rol: ${role}`,
    resend: "Reenviar",
    resent: "Invitación reenviada con un enlace nuevo.",
    revoke: "Revocar",
    confirmRevoke: (email: string) =>
      `¿Revocar la invitación de ${email}? Su enlace dejará de valer.`,
    confirmRevokeButton: "Sí, revocar",
    inviteTitle: "Invitar a alguien",
    inviteDescription: "Recibirá un enlace por email, válido durante 7 días.",
    email: "Email",
    roleLabel: "Rol",
    noOfferableRoles: "Tu rol no puede ofrecer ningún rol en una invitación.",
    invite: "Invitar",
    inviting: "Invitando…",
    invited: (email: string) => `Invitación enviada a ${email}.`,
    resendPending: "Reenviar la pendiente",
  },
  invite: {
    title: "Invitación",
    accepting: "Aceptando la invitación…",
    missing:
      "No hay ninguna invitación que aceptar. Abre de nuevo el enlace del email que recibiste.",
    toTenants: "Ir a mis organizaciones",
    logout: "Salir para entrar con otra cuenta",
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
  errorScreen: {
    title: "Algo ha fallado",
    description: "Ha ocurrido un error inesperado. Recarga la página o vuelve al inicio.",
    reload: "Recargar",
    home: "Ir al inicio",
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
    "membership.last_owner":
      "La organización se quedaría sin propietario. Haz propietario a otra persona antes.",
    "membership.changed":
      "Algún rol ha cambiado mientras tanto. Revisa la lista y vuelve a intentarlo.",
    "membership.target_forbidden":
      "Solo puedes gestionar a miembros con un rol por debajo del tuyo.",
    "membership.role_not_assignable": "Solo puedes asignar roles incluidos en el tuyo.",
    "membership.self_change":
      "No puedes cambiar tu propio rol ni expulsarte. Para irte, sal de la organización.",
    "membership.not_found": "Esa persona ya no es miembro de esta organización.",
    "membership.unknown_role": "Ese rol no existe.",
    "invitation.pending": "Ese email ya tiene una invitación pendiente. Puedes reenviarla.",
    "invitation.already_member": "Esa persona ya es miembro de la organización.",
    "invitation.role_not_offerable": "Solo puedes invitar con roles incluidos en el tuyo.",
    "invitation.revoked": "Esta invitación ha sido revocada.",
    "invitation.email_invalid": "Escribe un email válido, sin nombre ni signos < >.",
    "invitation.unknown_role": "Ese rol no existe.",
    "invitation.email_mismatch":
      "La invitación es para otro email. Sal, entra con la cuenta del email invitado y vuelve a abrir el enlace.",
    "invitation.already_accepted": "Esta invitación ya se ha aceptado.",
    "invitation.expired": "La invitación ha caducado. Pide que te la reenvíen.",
    "invitation.not_found":
      "La invitación no existe o su enlace ya no vale. Pide que te la reenvíen.",
    "tenant.not_active": "La organización no está activa.",
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
