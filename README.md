# vamosjuntos-backend

API en Go de Vamos Juntos .club. Corre en Cloud Run detrás de Firebase Hosting, que reescribe
`/api/**` hacia este servicio.

La especificación, el plan y los contratos viven en el repo
[vamosjuntos.club](https://github.com/snsilvam/vamosjuntos.club), en `specs/`.

## Variables de entorno

| Variable | Obligatoria | Descripción |
|----------|-------------|-------------|
| `EVENT_ID` | sí | Id del documento en la colección `events` de Firestore; los invitados se leen de `events/{EVENT_ID}/users`. |
| `GOOGLE_CLOUD_PROJECT` | sí | Proyecto de GCP y de Firebase; los ID tokens se validan contra él (`aud`). |
| `FIRESTORE_DATABASE` | no | Base de Firestore (por defecto `(default)`; en `vamosjuntos-club` es `vamosjuntos-club-db`). |
| `PORT` | no | Puerto HTTP (por defecto `8080`). |

El backend no tiene secretos: la autenticación la firma Google. `.env*` está en `.gitignore`.

## Autenticación

Los familiares inician sesión con Firebase Authentication (Google o correo y contraseña).

- **Token**: el frontend manda `Authorization: Bearer <ID token>` en cada llamada a `/api`. El
  backend lo verifica con el Admin SDK (`firebase.google.com/go/v4`): `401` si falta o no es
  válido.
- **Vínculo**: la primera vez, la cuenta se vincula a un invitado con su código. El vínculo es un
  documento en `events/{EVENT_ID}/links/{guestId}` con `uid`, `email` y `linkedAt`.
  - Una cuenta tiene un solo invitado y un invitado una sola cuenta.
  - Para deshacer un vínculo, el organizador borra ese documento en la consola.
- **Contrato**: está en `specs/008-autenticacion-firebase/contracts/api.md` del repo
  vamosjuntos.club.

## Invitados

El catálogo vive en Firestore, en `events/{EVENT_ID}/users`: un documento por invitado, con id
automático y los campos `Nombre` y `código`. El organizador lo mantiene a mano en la consola, y
la app solo lo lee.

- **Cuándo se lee**: en cada request que lo usa, así que los cambios aplican sin volver a
  desplegar. También se valida al arrancar.
- **Reglas**: de 1 a 25 invitados, nombres no vacíos y códigos únicos de 6 o más caracteres, sin
  distinguir mayúsculas.
  - Si el catálogo no cumple al arrancar, el proceso termina con un error que nombra a los
    invitados, nunca sus códigos.
  - Si deja de cumplir con el servicio en marcha, las operaciones responden `500` y el motivo
    queda en el log.
- **Campos extra**: se ignoran (p. ej. `confirmado`). La asistencia vive en
  `events/{EVENT_ID}/attendance`.
- **Seguridad**: las reglas de seguridad de Firestore niegan todo acceso desde clientes; solo el
  backend lee los códigos, con su cuenta de servicio.

## Desarrollo local

Requiere Go 1.26 y el emulador de Firestore (que a su vez necesita Java 8+). En el emulador hay
que crear el evento y sus invitados de prueba; la guía de abajo tiene los `curl`.

```bash
gcloud emulators firestore start --host-port=localhost:8081
# El proyecto debe ser el real aunque Firestore sea el emulador: los ID tokens de Firebase se
# validan contra él. Para iniciar sesión hace falta el frontend local con su .env.local.
export FIRESTORE_EMULATOR_HOST=localhost:8081 GOOGLE_CLOUD_PROJECT=vamosjuntos-club
export EVENT_ID=paseo-familiar-diciembre-2026
go run .
```

La guía completa, con la siembra del evento y los escenarios de validación, está en
`specs/001-onboarding-sesion/quickstart.md` del repo vamosjuntos.club.

## Validaciones

```bash
gofmt -l . && go vet ./... && golangci-lint run && go test ./...
```

## Despliegue

Flujo de ramas: rama de feature → `main` (integración) → `production` (despliegue). Un push a
`production` ejecuta [`.github/workflows/backend-deploy.yml`](.github/workflows/backend-deploy.yml),
que hace esto en orden:

1. Corre las validaciones de arriba. Si alguna falla, no despliega.
2. Se autentica en GCP con Workload Identity Federation. No hay llaves JSON.
3. Construye la imagen `us-central1-docker.pkg.dev/vamosjuntos-club/containers/vamosjuntos-backend:<commit>`.
4. La despliega en el servicio de Cloud Run `vamosjuntos-backend` (`us-central1`).

Un push a `main` o a cualquier otra rama no publica nada.

El pipeline no usa secrets: los invitados se leen de Firestore y la autenticación la firma
Firebase.

- **Publicar**: `git push origin main:production`.
- **Relanzar sin commits**: *Actions → Deploy to Cloud Run → Run workflow*, sobre `production`.
- **Cambiar el catálogo**: edita `events/{EVENT_ID}/users` en la consola de Firestore. No hace
  falta desplegar.
- **Revertir**: haz `git revert <commit>` en `main` y luego `git push origin main:production`.

La configuración inicial de GCP y los escenarios de validación están en
`specs/002-despliegue-continuo/quickstart.md` del repo vamosjuntos.club.
