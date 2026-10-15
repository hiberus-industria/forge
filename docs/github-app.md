# GitHub App

La API de forge usa una **GitHub App** instalada en `hiberus-industria` para:

- Sincronizar repos, issues y PRs.
- Recibir webhooks: merge, cierre y cambios de estado.
- Mover el campo *Status* del Project de cada repo.

Lo que hace el desarrollador (crear la rama, asignarse el issue, abrir el PR) **no** pasa por la App. Se hace en su máquina con su propio token de `gh`, para que la autoría en GitHub sea suya.

## Permisos

| Ámbito | Permiso (UI) | Clave en el manifest | Nivel | Motivo |
|---|---|---|---|---|
| Repository | Issues | `issues` | Read & write | Leer issues y añadirlos al Project. |
| Repository | Pull requests | `pull_requests` | Read | Detectar PR abierto o mergeado. |
| Repository | Metadata | `metadata` | Read | Obligatorio; listado de repos. |
| Organization | Members | `members` | Read | Comprobar que el GitHub vinculado pertenece a la org. |
| Organization | Projects | `organization_projects` | Read & write | Leer los Projects v2 y cambiar el *Status*. |

Las claves son las que acepta el objeto `permissions` de la API de GitHub Apps ([Create an installation access token](https://docs.github.com/en/rest/apps/apps#create-an-installation-access-token-for-an-app)).

## Eventos webhook

| Evento | Permiso necesario | Uso |
|---|---|---|
| `issues` | Issues | Alta, edición, asignación, cierre y reapertura. |
| `pull_request` | Pull requests | PR abierto (→ *In Review*) y mergeado (→ cierre y snapshot del coste). |
| `repository` | Metadata | Repos creados, renombrados o archivados. |
| `projects_v2_item` | Projects (org) | Cambios de *Status* hechos a mano en el Project. |

Notas:

- `installation_repositories` se envía **siempre** a las GitHub Apps y no se puede suscribir manualmente. forge lo trata como "repo cambiado".
- Los webhooks de Projects v2 están en *public preview* en GitHub y la documentación no deja del todo clara su disponibilidad para Apps. La reconciliación periódica de la API cubre esos cambios aunque no lleguen por webhook.

Referencia: [Webhook events and payloads](https://docs.github.com/en/webhooks/webhook-events-and-payloads).

## Crear la App

### Opción A: desde el manifest

1. Edita [`github-app-manifest.json`](github-app-manifest.json) y sustituye `forge.example.com` por el host real de la API.
2. Sigue el flujo de [registro desde un manifest](https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest): un formulario HTML hace POST del manifest a `https://github.com/organizations/hiberus-industria/settings/apps/new`, y GitHub devuelve un `code` que se canjea por las credenciales.

### Opción B: a mano

En *Organization settings → Developer settings → GitHub Apps → New GitHub App*:

1. **Name**: `forge (hiberus-industria)`. **Homepage**: la URL del repo.
2. **Webhook**: activo.
   - **URL**: `https://<host>/webhooks/github`.
   - **Secret**: genera uno aleatorio (`openssl rand -hex 32`).
3. **Permissions** y **Subscribe to events**: los de las tablas anteriores.
4. **Where can this GitHub App be installed?**: *Only on this account*.
5. Crea la App y apunta el **App ID**.
6. Genera una **private key** (`.pem`).
7. Instálala en la org: *Install App → hiberus-industria → All repositories*. Recomendado, porque forge descubre los repos solo. El **installation id** es el número final de la URL de la instalación: `.../settings/installations/<id>`.

## Configuración de la API

```sh
FORGE_GITHUB_ORG=hiberus-industria
FORGE_GITHUB_APP_ID=123456
FORGE_GITHUB_INSTALLATION_ID=7890123
FORGE_GITHUB_PRIVATE_KEY_FILE=/run/secrets/github-app.pem
FORGE_GITHUB_WEBHOOK_SECRET=<secret>
```

En la VM, guarda el `.pem` en `deploy/secrets/` (ignorado por git) y móntalo como fichero de solo lectura. Ver [deploy.md](deploy.md).

## Projects por repo

Cada repo tiene su propio Project v2 con su campo *Status*. La API detecta las opciones por nombre: *Todo/Backlog*, *In progress*, *In review*, *Done* y equivalentes en castellano. Si alguna no encaja, un admin la corrige desde la TUI (pantalla de repos) o con `PUT /v1/repos/{owner}/{repo}/status-map`.
