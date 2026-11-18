package mcpserver

// Las descripciones son código de producción: son el único canal que el agente
// lee siempre. Se testean (ver descriptions_test.go) para que no pierdan lo
// obligatorio en un refactor.
const (
	descSearch = `Busca en la web y devuelve resultados estructurados.

Campos de cada resultado: title, url, snippet (100-200 caracteres, para mostrar), summary (600-800 caracteres, para leer), content (vacío salvo que pidas need_content), site_name, site_icon, date (RFC 3339 en UTC o null), authority (0 a 4; -1 = desconocido; 3 o más es fuente muy autoritativa) y rerank_score (0 a 3; desde 1.5 se considera resultado de calidad).

Parámetros:
- query (obligatorio): 1 a 128 caracteres, no puede ser solo espacios. Acepta operadores: site:dominio, -site:dominio, -término, -"frase exacta", "frase exacta", lang:es,en, after:2025, before:2026.
- count: 1 a 50. Si lo omitís, la API devuelve 10. OJO: pedir 50 casi nunca vale la pena — de una sola query rara vez hay más de diez URLs útiles, y las de abajo son ruido. Para cubrir más terreno, hacé varias queries con enfoques distintos en vez de subir count.
- need_content: por defecto false. Con true cada resultado trae el texto de la página en content y te ahorra el fetch, pero puede devolver varios megabytes.
- ori_question: la pregunta original del usuario (1 a 128 caracteres); mejora la comprensión de la query.
- localization: una dirección IP para localizar resultados (no un código de país).
- min_rerank_score: filtro local opcional de 0 a 3. No es un parámetro de la API: el servidor descarta los resultados por debajo del umbral e informa cuántos filtró.

Si no hay coincidencias devuelve una lista vacía: eso es un resultado, no un error.`

	descFetch = `Trae una página web y devuelve su título y el contenido en Markdown.

Parámetro: url (obligatorio), HTTP o HTTPS, con host, sin usuario ni contraseña, hasta 8192 caracteres.

Esta tool entrega el contenido por ventana, porque una página larga entera desborda tu contexto. max_chars y offset NO son parámetros de la API: son una ventana local sobre la respuesta que ya llegó. Si la respuesta viene recortada, vas a ver truncated=true, el total real y next_offset; llamá de nuevo con ese offset para seguir.

OJO: cada llamada a fetch se factura, incluso la que solo pide la continuación. No pagines de a poco: si el artículo ya respondió tu pregunta, no sigas.`

	descOCR = `Reconoce el texto de un documento o una imagen y lo devuelve página por página.

Parámetros:
- path (obligatorio): ruta de un archivo LOCAL. No es una URL; si tenés una URL, usá fetch o descargá el archivo primero. El servidor lee el archivo de tu disco, así que solo pasá rutas que el usuario autorizó.
- purpose (obligatorio, sin default): text_recognition extrae el texto directamente de Word y PowerPoint sin imágenes y reconoce el resto página por página; rich_layout_recognition reconoce todas las páginas respetando el layout.
- from_page y max_pages: ventana local de páginas (por defecto 50). El tope del documento es 1000 páginas y 100 MiB.

Formatos: PDF, Word (DOC, DOCX), PowerPoint (PPT, PPTX) e imágenes PNG, JPEG, WebP, GIF, BMP y TIFF. El formato se detecta por el contenido, no por la extensión.

Dos advertencias. Primero: cancelar esta llamada NO cancela el reconocimiento ni el cobro; una vez que el archivo se envió, el trabajo termina del lado del servicio y se factura por página reconocida. Segundo: el texto vuelve crudo, con tokens de posición como <|ref|> y <|box|> y coordenadas normalizadas de 0 a 1000; no están limpios y son parte del resultado. Cada página trae su finish_reason: si es "length", la página quedó incompleta del lado del servicio.`
)

// Instructions viaja una vez en el initialize y queda en el contexto del
// agente. Tiene un tope de 2.000 caracteres: se paga en cada sesión.
const instructions = `Servidor zond-mcp: tres tools sobre la API de Tools de DeepSeek.

Patrón recomendado, en orden de costo:
1. search con count moderado (5 a 10) y need_content=false. Mirá rerank_score y authority para elegir; desde 1.5 y 3 respectivamente son buenas señales.
2. Usá min_rerank_score si la lista trae ruido, en vez de descartar a mano.
3. fetch solo de las URLs que valgan la pena. La respuesta viene por ventana: si trae truncated, decidí si el resto te sirve antes de pedirlo, porque cada llamada se factura.
4. ocr es para archivos locales. Recibe una ruta, no una URL.

Nunca subas count a 50 esperando más cobertura: de una query salen pocas URLs útiles, y las de abajo suelen ser ruido. Varias queries distintas cubren más que una query con quantity.

Si una tool falla, el texto del error dice qué pasó y qué hacer: distinguí "sin resultados" (éxito con lista vacía) de "falló la llamada" (isError). No reintentes un fallo marcado como no reintentable: los de saldo, autenticación y el timeout de OCR no se arreglan repitiendo.

El costo estimado viene en la línea de metadatos de cada respuesta.`
