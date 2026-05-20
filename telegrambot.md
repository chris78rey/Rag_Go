# Como probar que todo funciona

## Flujo de usuario

Una vez que tu servidor este arriba con el nuevo token, el bot estara completamente operativo. Sigue estos pasos para verificar el flujo multiusuario y el aislamiento de datos.

### 1. Inicia el chat

Entra a Telegram, busca a `@Conectateragbot` y presiona **Iniciar** o envia el comando:

```text
/start
```

### 2. Mensaje de bienvenida

Si tu ID de Telegram todavia no esta vinculado, el bot detectara eso y respondera automaticamente con un mensaje como:

> Bienvenido. Para empezar a consultar tus documentos, primero debes vincular tu cuenta.

### 3. Vincula tu usuario web

Escribe en el chat el comando de inicio de sesion, usando un usuario que ya exista en tu panel web:

```text
/login admin@rag.local tu_contrasena_aqui
```

### 4. Confirmacion exitosa

Si las credenciales coinciden, el bot guardara la relacion en la tabla **`user_telegram_links`** y te respondera algo como:

> Cuenta vinculada con exito.

### 5. Prueba la consulta RAG

A partir de ese momento, cualquier pregunta que envies activara el flujo RAG. Por ejemplo:

> Cual es la politica de seguridad descrita en los manuales?

El sistema va a:

1. Vectorizar tu texto.
2. Buscar en Qdrant solo tus documentos.
3. Consultar a OpenRouter.
4. Entregar la respuesta en tu celular en segundos.

## Resultado esperado

Si todo esta bien configurado, deberias ver:

- **Bot activo** en Telegram.
- **Vinculacion correcta** entre tu usuario web y tu chat.
- **Respuestas privadas** con aislamiento por usuario.
- **Consultas funcionales** sobre tus documentos.
