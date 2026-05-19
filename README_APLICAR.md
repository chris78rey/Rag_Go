# Deploy con SQLite

La aplicacion ya no usa PostgreSQL. Ahora funciona con SQLite local y con menos piezas para desplegar.

Componentes:
- `server` en Go
- `qdrant`
- volumen `uploads`
- volumen `data` para `data/rag.db`

## Variable obligatoria

Solo necesitas esta variable externa:

```env
OPENROUTER_API_KEY=tu_clave_real
```

El resto usa valores por defecto dentro del binario Go.

## Local

```powershell
copy .env.example .env
docker compose up -d --build
```

Abrir:

```text
http://localhost:8080
```

## VPS

```powershell
copy .env_vps.example .env_vps
docker compose -f docker-compose_vps.yml up -d --build
```

Si usas Traefik o Coolify, el dominio y las etiquetas siguen configurados en `docker-compose_vps.yml`.

## Usuario inicial

```text
Email: admin@rag.local
Password: admin123
```

## Notas

- La UI ya viene embebida dentro del binario Go.
- Los archivos subidos se guardan en `uploads`.
- La base SQLite persiste en `data/rag.db`.
