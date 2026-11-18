# zond-mcp

Servidor MCP que expone las **tres tools de contenido de DeepSeek** —búsqueda web, lectura de
páginas y OCR— con una idea simple: el agente recibe datos, no interpretaciones, y cuando algo
falla se le dice qué hacer.

*Zond* (зонд) es "sonda" en ruso. Es lo que hace: mete una sonda en la web y en tus documentos,
y te devuelve lo que encontró, con el costo y los límites a la vista.

---

## Qué hace

| Tool | Endpoint | Para qué |
|---|---|---|
| `search` | `POST /v1/tools/search` | Lista de resultados rankeados con score, autoridad, fecha y resumen. Puede traer el texto de cada página. |
| `fetch` | `POST /v1/tools/fetch` | Una URL → título y contenido en Markdown, por ventana y con continuación. |
| `ocr` | `POST /v1/tools/ocr` | Un archivo local (PDF, Word, PowerPoint o imagen) → texto página por página. |

## Por qué existe

No somos los primeros: hay al menos cuatro MCP que envuelven estos endpoints y cinco más en Go
que cubren búsqueda y lectura. Ninguno hace OCR, y el único que lo intenta falla justo las tres
cosas que la guía de DeepSeek advierte.

Estas son las cuatro que este servidor sí hace:

**No inventa defaults.** Si no mandás `count`, no se manda `count` y la API devuelve lo suyo.
**Declara los recortes.** Una página larga se entrega por ventana, con el total real y el puntero
para seguir. Nunca se corta en silencio. **Traduce los errores.** Cada fallo dice qué pasó y qué
hacer, y distingue lo que se arregla reintentando de lo que no. **Trata el OCR como corresponde.**
En OCR un `200` puede traer un error adentro, desconectar no cancela el reconocimiento, y el
timeout no se reintenta porque se cobra igual.

## Instalación

Necesitás una cuenta de DeepSeek con saldo y una clave de `platform.deepseek.com/api_keys`.
El servidor es un binario único: no hay runtime, ni Docker, ni `npm install`.

1. Bajá el binario de tu plataforma desde *Releases* y dejdalo en una carpeta que no vayas a mover:

   | Sistema | Archivo |
   |---|---|
   | Windows | `zond-mcp-windows-amd64.exe` |
   | macOS (Apple Silicon) | `zond-mcp-darwin-arm64` |
   | Linux x86_64 | `zond-mcp-linux-amd64` |

2. Verificá que funciona antes de tocar la configuración:

   ```bash
   ./zond-mcp --version
   ```

3. Declaralo en el cliente MCP. La ruta del binario va completa; **la clave va en `env`**, nunca en
   la línea de comandos.

   **Windows** — `%APPDATA%\Claude\claude_desktop_config.json` (o el equivalente de tu cliente):

   ```json
   {
     "mcpServers": {
       "zond": {
         "command": "C:\\Tools\\zond-mcp-windows-amd64.exe",
         "env": { "DEEPSEEK_API_KEY": "PEGÁ TU CLAVE ACÁ" }
       }
     }
   }
   ```

   **macOS y Linux**:

   ```json
   {
     "mcpServers": {
       "zond": {
         "command": "/usr/local/bin/zond-mcp",
         "env": { "DEEPSEEK_API_KEY": "PEGÁ TU CLAVE ACÁ" }
       }
     }
   }
   ```

4. Reiniciá el cliente y corré el chequeo, que no gasta ninguna consulta:

   ```bash
   DEEPSEEK_API_KEY=tu-clave ./zond-mcp --check
   ```

   Debe decir tres cosas: configuración, conectividad y saldo. Si dice `×`, el texto explica qué
   arreglar.

### Si algo sale mal

Borrá la entrada que agregaste al JSON y todo vuelve a como estaba. El servidor no instala nada,
no escribe archivos ni deja procesos corriendo.

Un servidor que arranca **no** es un servidor que funciona: si la clave falta o está mal, el
servidor arranca igual y el error aparece en la primera llamada, con la variable nombrada.

## Cómo usarlo

El patrón barato, que el servidor le enseña al agente en sus descripciones:

1. `search` con `count` de 5 a 10 y `need_content: false`. Elegí por `rerank_score` (desde 1,5 es
   buena señal) y `authority` (desde 3 es fuente autoritativa).
2. Si la lista trae ruido, `min_rerank_score` en vez de descartar a mano.
3. `fetch` solo de las URLs que valgan la pena. Si viene `truncated`, decidí si querés el resto
   **antes** de pedirlo: cada llamada se factura.
4. `ocr` para archivos locales. Recibe una **ruta**, no una URL.

**No subas `count` a 50 esperando más cobertura.** De una sola query salen pocas URLs útiles y las
de abajo son ruido; varias queries con enfoques distintos cubren más que una query con cantidad.

### Variables de entorno

| Variable | Obligatoria | Default | Para qué |
|---|---|---|---|
| `DEEPSEEK_API_KEY` | sí | — | Clave de la cuenta. Solo por entorno. |
| `DEEPSEEK_BASE_URL` | no | `https://api.deepseek.com` | Apuntar a un gateway propio. |
| `DEEPSEEK_LOG_LEVEL` | no | `warn` | `debug`, `info`, `warn` o `error`. Va a stderr. |

### Costo

Precios publicados por DeepSeek al **2026-11-16**, en dólares:

- `search` y `fetch`: **USD 0,75 por cada 1.000 requests exitosas**. No se cobran los fallos ni
  las búsquedas sin resultados (`count` no cambia el precio).
- `ocr`: **USD 0,225 por cada 1.000 páginas** reconocidas. Se cobra por página reconocida, no por
  request; un reconocimiento que falla no se cobra.

Cada respuesta trae un `estimated_cost_usd` **estimado**, calculado con esas tarifas. No es la
factura: para el número exacto está el panel de DeepSeek. Si los precios cambian, el número en la
respuesta queda viejo hasta que se actualicen las constantes del código.

## Sobre `ocr` y tus archivos

La tool recibe una ruta y el servidor **lee ese archivo de tu disco**. La confianza está puesta en
el agente y en vos: si no querés que un agente pueda leer archivos de tu máquina, no habilites
`ocr`. El servidor rechaza enlaces simbólicos —también los que estén en un directorio del camino—, directorios
y dispositivos, y no envía más de 100 MiB por archivo, pero no tiene una lista blanca de carpetas
(todavía). En Windows la detección cubre enlaces simbólicos, no necesariamente junctions.

Dos avisos que salen de la documentación de DeepSeek y que conviene tener claros:

- **Cancelar una llamada a `ocr` no cancela el reconocimiento ni el cobro.** Una vez que el archivo
  se envió, el trabajo termina del lado del servicio y se factura por página.
- El texto vuelve **crudo**, con tokens de posición como `<|ref|>` y `<|box|>` y coordenadas
  normalizadas de 0 a 1000. Cada página trae su `finish_reason`; si es `length`, la página quedó
  incompleta del lado del servicio.

## Qué no hace

ASR y TTS. El endpoint compatible con Anthropic y su `web_search` (devuelve texto redactado, no
estructura). Transporte HTTP: es un servidor local por stdio. Caché o estado de cualquier clase.
Reverta o intermediación del servicio: cada usuario paga su propia cuenta.

## Desarrollo

```bash
gofmt -l .            # tiene que no imprimir nada
go vet ./...
go test ./...         # unitarios, sin red y sin clave
go test -tags integration ./...   # contra la API real; necesita DEEPSEEK_API_KEY con saldo
```

El contrato con el que se construyó es la documentación oficial de DeepSeek: los comentarios del
código citan la página de origen de cada regla, y los tests unitarios no tocan la red. Las pruebas
de integración existen para que un cambio de contrato de la API falle ruidosamente.

## Licencia y aviso

MIT. **No está afiliado a DeepSeek**: los términos, precios y límites de la API son de ellos.
La documentación de origen —tablas de límites, errores y precios— está citada en los comentarios
del código, con la fecha en que se verificó.
