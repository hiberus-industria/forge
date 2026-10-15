# Guía del desarrollador

forge relaciona lo que gastas en IA (Claude Code, OpenCode) con el issue de GitHub en el que estás trabajando. No cambia tu forma de trabajar: basta con seguir la convención de ramas.

## 1. Instalación

```sh
curl -fsSL https://raw.githubusercontent.com/hiberus-industria/forge/main/install.sh | sh
```

El repo es privado. El script usa `gh` si lo tienes autenticado (`gh auth login`); si no, `GH_TOKEN`. Instala en `~/.local/bin/forge`. Para fijar una versión: `FORGE_VERSION=v1.2.0`.

## 2. Primer arranque

```sh
forge login            # abre el navegador (Keycloak) y vincula tu cuenta de GitHub
forge setup            # muestra el diff y conecta Claude Code y OpenCode al daemon
forge daemon install   # instala el daemon como servicio de usuario (systemd / launchd)
forge doctor           # comprueba que todo está bien
```

- `forge login` usa el navegador. En una máquina sin navegador (SSH), usa el código de dispositivo: abres la URL que te muestra en cualquier otro equipo e introduces el código.
- `forge setup` solo toca la configuración de tus harnesses: `~/.claude/settings.json` y el plugin global de OpenCode. Antes de aplicar te enseña el diff, guarda copia de seguridad de cada fichero y se puede revertir con `forge setup --remove`. Reinicia las sesiones abiertas de Claude Code y OpenCode después.

## 3. Día a día

```sh
forge start 123      # crea feat/123-<slug>, te asigna el issue y lo pasa a "In Progress"
claude               # o opencode: trabaja con normalidad; el gasto se asigna solo al #123
forge pr             # abre el PR con "Closes #123" y pasa el issue a "In Review"
# merge en GitHub → el issue se cierra y el coste queda registrado (snapshot)
```

También puedes hacerlo todo desde la TUI: ejecuta `forge`, elige el issue en **Mis tareas** y pulsa la tecla de start o pr.

### Convención de ramas

```
<tipo>/<issue>-<slug>      feat/123-login-oidc · fix/88-null-cost · docs/12-guia
```

Tipos: `feat`, `fix`, `chore`, `docs`, `refactor`, `test`, `spike`. `forge start` deduce el tipo de las labels del issue. También se reconocen las ramas `123-slug` que crea `gh issue develop`.

### ¿Y si no hay issue?

- Si trabajas en `main` o en una rama sin número, el gasto va a la **bandeja sin asignar**, asociado al repo.
- Si trabajas fuera de un repo de la org, va como "personal/otros" y no se guarda ningún dato de ese repo.
- Desde la **Bandeja** de la TUI puedes reasignar sesiones enteras a un issue. Solo puedes reasignar lo tuyo; un admin puede reasignar cualquier cosa. Todo queda auditado.
- Para forzar una tarea un rato (spike, revisión de un PR ajeno), fija un **override** desde la TUI. Lo que gastes mientras esté activo se asigna a ese issue (origen "manual").

## 4. Informes

En la TUI, **Informes** agrupa por repo, issue, desarrollador, modelo o día, en el periodo que elijas, con importes en USD y EUR (tipo de cambio del BCE de cada día). Se pueden exportar a CSV. Todo el equipo ve todos los datos.

## 5. Privacidad

forge **solo envía metadatos**: modelo, tokens, fecha y hora, ids de sesión y request, repo, rama e issue. **Nunca** envía prompts, respuestas, código ni rutas de tu disco. El daemon filtra antes de enviar y la API rechaza cualquier campo que no conozca.

## 6. Problemas frecuentes

Lo primero siempre es `forge doctor`: indica qué falla y cómo arreglarlo.

| Síntoma | Qué mirar |
|---|---|
| La TUI dice "daemon no está corriendo" | `forge daemon install`, o `forge daemon run` en primer plano para ver los logs. |
| No aparece gasto nuevo | Reinicia la sesión del harness después de `forge setup`. Comprueba en `forge status` la hora del último evento de cada harness. |
| Todo cae en la bandeja | Revisa que la rama siga la convención (`git branch --show-current`). |
| Eventos pendientes que no se sincronizan | `forge status` muestra el último error; `forge login` si caducó la sesión. |
| Coste 0 o "sin precio" en algún modelo | Ese modelo no está en la tabla de precios; pide a un admin que añada el precio manualmente. |

Logs del daemon: `~/.local/state/forge/daemon.log` (macOS: `~/Library/Application Support/forge/state/daemon.log`).

## 7. Notas para WSL2

- **Keyring**: WSL2 normalmente no tiene Secret Service (gnome-keyring). En ese caso forge guarda las credenciales en `~/.local/state/forge/credentials.json` con permisos `0600`, y `forge doctor` te lo indica.
- **systemd**: `forge daemon install` necesita systemd. Actívalo en `/etc/wsl.conf` y reinicia con `wsl --shutdown`:

  ```ini
  [boot]
  systemd=true
  ```

  Sin systemd, usa `forge daemon run` en una terminal o tmux.
- **Navegador**: `forge login` intenta abrir el navegador de Windows (`wslview` o `explorer.exe`). Si no lo consigue, copia la URL que muestra o usa el código de dispositivo.
- Instala y ejecuta Claude Code y OpenCode **dentro de WSL**, que es donde corre el daemon.
