# Keycloak

forge usa Keycloak como proveedor de identidad. Hacen falta **dos clients** en el realm:

| Client | Tipo | Para qué |
|---|---|---|
| `forge-cli` | Público (app nativa) | Login del desarrollador desde la CLI. Lo usan también el daemon y la TUI. |
| `forge-api` | Sin flujos (resource server) | Audiencia de los tokens y contenedor de los roles `member` y `admin`. |

Hay un realm de ejemplo listo para importar en [`deploy/keycloak/forge-realm-example.json`](../deploy/keycloak/forge-realm-example.json). Lo usa `make dev-keycloak`, que levanta Keycloak en `http://localhost:8180`, realm `hiberus`, con los usuarios `dev/dev` (admin) y `member/member`.

> Esta configuración está verificada contra Keycloak **26.8.1**: login por loopback con puerto aleatorio, Device Grant con PKCE, y `aud` y roles presentes en el access token.

## 1. Client `forge-api` (resource server)

- **Client authentication**: On (confidencial). No necesita secreto en uso, porque nadie hace login con él.
- **Desactivar todos los flujos**: Standard flow, Implicit, Direct access grants, Service accounts y OAuth 2.0 Device Authorization Grant.
- **Roles** (pestaña *Roles*):
  - `member`: puede leerlo todo y reasignar su propio gasto.
  - `admin`: gestiona precios y mapeos de estado, y reasigna cualquier gasto.

La API valida los JWT así:

- Firma, contra el JWKS de `<issuer>/protocol/openid-connect/certs`.
- `iss` igual a `FORGE_OIDC_ISSUER`.
- `aud` contiene `forge-api`.
- Token no caducado.
- Roles leídos de `resource_access.forge-api.roles`.

## 2. Client `forge-cli` (público, nativo)

| Ajuste | Valor | Atributo en el JSON del realm |
|---|---|---|
| Client authentication | **Off** (público) | `"publicClient": true` |
| Standard flow | **On** | `"standardFlowEnabled": true` |
| Direct access grants | Off | `"directAccessGrantsEnabled": false` |
| OAuth 2.0 Device Authorization Grant | **On** | `attributes."oauth2.device.authorization.grant.enabled": "true"` |
| PKCE Method | **S256** | `attributes."pkce.code.challenge.method": "S256"` |
| Valid redirect URIs | `http://127.0.0.1/callback` | `"redirectUris": ["http://127.0.0.1/callback"]` |
| Web origins | (vacío) | `"webOrigins": []` |
| Front channel logout | Off | `"frontchannelLogout": false` |
| Optional client scopes | `offline_access` | `"optionalClientScopes": ["offline_access"]` |

### Redirect por loopback y puertos (RFC 8252)

La CLI abre un servidor temporal en `http://127.0.0.1:<puerto aleatorio>/callback`. **Registra la URI sin puerto.** Si la redirect usa `http` sobre una interfaz loopback (`localhost`, `127.0.0.1` o `[::1]`), Keycloak quita el puerto antes de compararla con las URIs registradas. Así, `http://127.0.0.1/callback` acepta cualquier puerto. Este comportamiento está en `RedirectUtils.java` del código de Keycloak.

Se comprobó con la instancia de desarrollo:

- `http://127.0.0.1:53123/callback` → se acepta.
- `http://127.0.0.1:53123/evil` → se rechaza (*Invalid parameter: redirect_uri*), porque la ruta se compara de forma exacta.
- `http://localhost:53123/callback` → se rechaza, porque el host registrado es `127.0.0.1`. La CLI debe usar la IP literal, que es lo que recomienda RFC 8252 §8.3.

No uses comodines (`http://127.0.0.1/*`). Una ruta fija reduce la superficie de ataque.

### PKCE también en el Device Grant

Con `pkce.code.challenge.method = S256`, Keycloak **exige PKCE también en la petición de Device Authorization** (`/protocol/openid-connect/auth/device`). Sin él devuelve `Missing parameter: code_challenge_method`. La CLI tiene que enviar `code_challenge`/`code_challenge_method` en esa petición y `code_verifier` al canjear el `device_code`.

### Audiencia

Añade al client `forge-cli` un mapper **Audience** (`oidc-audience-mapper`) con:

- *Included Client Audience* = `forge-api`.
- *Add to access token* = On.
- *Add to ID token* = Off.

Resultado verificado: el access token lleva `"aud": "forge-api"` y `"azp": "forge-cli"`.

### Roles en el access token

El client scope por defecto `roles` incluye el mapper *client roles*, que emite `resource_access.<client>.roles`. Con `fullScopeAllowed: true` en `forge-cli`, el token incluye:

```json
"resource_access": { "forge-api": { "roles": ["member", "admin"] } }
```

Si limitas el scope de `forge-cli` (*Full scope allowed* = Off), añade los roles de `forge-api` en la pestaña *Client scopes → Dedicated scope → Scope* de `forge-cli`.

## 3. Grupos (recomendado)

En lugar de asignar roles a cada usuario, crea dos grupos:

- `forge-members` → rol `member` del client `forge-api`.
- `forge-admins` → roles `member` y `admin` del client `forge-api`.

Si el equipo ya tiene un grupo en Keycloak, basta con darle el rol `member` a ese grupo.

## 4. Sesiones y rotación de refresh tokens

- La CLI pide el scope `offline_access`: el daemon guarda un **offline token** y el desarrollador no tiene que volver a hacer login a diario. La caducidad la marca *Offline Session Idle* (en el ejemplo, 30 días). Cada refresco renueva la sesión.
- Los usuarios necesitan el rol de realm **`offline_access`**. Suele venir dentro de `default-roles-<realm>`; si no, Keycloak responde `not_allowed: Offline tokens not allowed for the user or client` y la CLI muestra `Device code not valid` o falla el login. El realm de ejemplo lo asigna explícitamente.
- **Revoke Refresh Token = On** y **Refresh Token Max Reuse = 0**: cada refresco devuelve un token nuevo y el anterior deja de valer. Es seguro porque **solo el daemon refresca**. La TUI y los comandos de la CLI le piden el access token al daemon por el socket unix, así que no hay carreras entre procesos.
- Access token corto (5 min en el ejemplo).
- Para revocar el acceso de alguien: *Users → Consents* (offline tokens) o desactivar al usuario.

## 5. Configuración del lado forge

CLI (`~/.config/forge/config.toml` o valores incluidos en el binario):

```toml
[oidc]
issuer    = "https://sso.example.com/realms/hiberus"
client_id = "forge-cli"
```

API (variables de entorno):

```sh
FORGE_OIDC_ISSUER=https://sso.example.com/realms/hiberus
FORGE_OIDC_AUDIENCE=forge-api
```
