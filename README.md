# vamosjuntos-backend

API en Go de Vamos Juntos .club. Corre en Cloud Run detrás de Firebase Hosting, que reescribe
`/api/**` hacia este servicio.

La especificación, el plan y los contratos viven en el repo
[vamosjuntos.club](https://github.com/snsilvam/vamosjuntos.club), en `specs/`.

## Variables de entorno

| Variable | Obligatoria | Descripción |
|----------|-------------|-------------|
| `GUESTS_JSON` | sí | Catálogo `[["Nombre","código"], ...]`: máximo 25 invitados, códigos únicos de 6 o más caracteres. **Secreto.** |
| `SESSION_KEY` | sí | Llave HMAC en base64 que decodifica a 32 bytes (`openssl rand -base64 32`). **Secreto.** |
| `EVENT_ID` | sí | Id del documento en la colección `events` de Firestore. |
| `GOOGLE_CLOUD_PROJECT` | no | Proyecto de GCP; en Cloud Run se detecta solo. |
| `FIRESTORE_DATABASE` | no | Base de Firestore (por defecto `(default)`; en `vamosjuntos-club` es `vamosjuntos-club-db`). |
| `PORT` | no | Puerto HTTP (por defecto `8080`). |

Los secretos nunca se versionan: `.env*` está en `.gitignore`.

## Desarrollo local

Requiere Go 1.26 y el emulador de Firestore (que a su vez necesita Java 8+).

```bash
gcloud emulators firestore start --host-port=localhost:8081
export FIRESTORE_EMULATOR_HOST=localhost:8081 GOOGLE_CLOUD_PROJECT=vamosjuntos-local
export EVENT_ID=paseo-familiar-diciembre-2026 SESSION_KEY=$(openssl rand -base64 32)
export GUESTS_JSON='[["Carmencita","prueba01"],["Liam","prueba02"]]'
go run .
```

La guía completa, con la siembra del evento y los escenarios de validación, está en
`specs/001-onboarding-sesion/quickstart.md` del repo vamosjuntos.club.

## Validaciones

```bash
gofmt -l . && go vet ./... && golangci-lint run && go test ./...
```
