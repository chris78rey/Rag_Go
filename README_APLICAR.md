# Patch listo para levantar Semantic Core RAG con interfaz web

Este ZIP contiene archivos para completar el proyecto actual.

## Archivos incluidos

```text
cmd/server/main.go
docker-compose.yml
Dockerfile
.env.example
internal/webui/index.html
internal/vectorstore/qdrant.go
internal/api/handlers.go
```

## Qué corrige

1. Agrega el archivo `cmd/server/main.go`, necesario porque el `Dockerfile` compila `./cmd/server`.
2. Agrega `docker-compose.yml` con:
   - server Go
   - PostgreSQL
   - Qdrant
3. Sirve la interfaz web desde `http://localhost:8080`.
4. Registra rutas administrativas:
   - `GET /api/admin/users`
   - `POST /api/admin/users`
   - `PATCH /api/admin/users/{id}`
   - `PATCH /api/admin/users/{id}/plan`
   - `GET /api/admin/usage`
   - `GET /api/admin/users/{id}/documents`
5. Embebe la interfaz web dentro del binario Go para evitar depender de una capa web separada en el VPS.
6. Corrige el ID de puntos en Qdrant para que sea un UUID válido.
7. Actualiza el campo `chunks` en PostgreSQL cuando termina la ingesta.

## Cómo aplicar en PowerShell

Desde la raíz del proyecto:

```powershell
Expand-Archive .\rag_go_web_ready_patch.zip -DestinationPath . -Force
copy .env.example .env
notepad .env
```

Colocar la clave real:

```env
OPENROUTER_API_KEY=sk-or-v1-tu-clave-real
```

Levantar:

```powershell
docker compose down
docker compose up --build
```

Abrir:

```text
http://localhost:8080
```

## Usuario inicial

```text
Email: admin@rag.local
Password: admin123
```

## Nota importante

La dimension de embeddings ya no se define manualmente con `EMBEDDING_DIM`.
El backend infiere la dimension desde `EMBEDDING_MODEL`.

Por defecto se usa `openai/text-embedding-3-large`, con dimension 3072.
