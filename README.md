# forge

**Trazabilidad del gasto IA por repo e issue**, para equipos que trabajan con GitHub y usan Claude Code y OpenCode.

Cada plataforma te dice cuánto has gastado, pero ninguna te dice **en qué**. forge captura cada request de tus harnesses en tu máquina, la asigna al issue en el que estás trabajando (a partir de la rama) y lo centraliza todo. Así cada coste tiene su repo, su issue y su desarrollador, sin cambiar la forma de trabajar.

> 📸 *Capturas de la TUI: pendiente de añadir (`make demo`).*

## Cómo funciona

- **TUI** (Bubble Tea): mis tareas con start/pr, coste en vivo de la sesión, bandeja de gasto sin asignar e informes en USD/EUR.
- **Daemon local**: recibe la telemetría de Claude Code (OTLP y hooks) y de OpenCode (plugin), detecta repo y rama en el momento de cada request, guarda un buffer offline en SQLite y sincroniza.
- **API central**: ingesta idempotente, precios (models.dev con overrides manuales), tipos de cambio del BCE, ciclo de vida con la GitHub App y una API REST documentada en OpenAPI, lista para una futura SPA.
- **Identidad**: Keycloak con PKCE y loopback (o código de dispositivo). Solo se envían **metadatos**; nunca prompts ni código.

```
forge start 123  →  trabajar con Claude Code / OpenCode  →  forge pr  →  merge
     │                         │                                │          │
 rama feat/123-x       gasto → issue #123              In Review   Done + snapshot del coste
```

## Inicio rápido (desarrolladores)

```sh
curl -fsSL https://raw.githubusercontent.com/hiberus-industria/forge/main/install.sh | sh
forge login
forge setup
forge daemon install
forge
```

Guía completa: [docs/developer-guide.md](docs/developer-guide.md).

## Documentación

- [Arquitectura y registro de decisiones](docs/architecture.md)
- [Configurar Keycloak](docs/keycloak.md)
- [Crear la GitHub App](docs/github-app.md)
- [Desplegar la API](docs/deploy.md)
- [Contrato de la API (OpenAPI)](api/openapi.yaml)

## Desarrollo

Requisitos: Go 1.26+, Docker (para los tests de integración y los servicios locales) y `golangci-lint` v2.

```sh
make help           # lista de tareas
make generate       # regenera el código desde api/openapi.yaml
make build          # bin/forge y bin/forge-api
make test           # tests (incluye integración con testcontainers)
make test-short     # solo tests unitarios
make lint
make dev-keycloak   # Keycloak local en :8180 (realm hiberus, usuario dev/dev)
make run-api        # API + Postgres con docker compose
make demo           # TUI con datos de demostración
```

Estructura: `internal/domain` (modelo puro) · `internal/app` (casos de uso y puertos) · `internal/adapters/*` (harnesses, GitHub, Keycloak, Postgres, SQLite...) · `cmd/forge` (CLI, TUI y daemon) · `cmd/forge-api`.
