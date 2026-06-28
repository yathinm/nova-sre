FROM node:22-alpine AS build

WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM nginx:1.27-alpine

COPY --from=build /app/dist /usr/share/nginx/html

EXPOSE 80

CMD ["/bin/sh", "-c", "escaped=$(printf '%s' \"${NOVA_SRE_API_BASE:-}\" | sed 's/\\\\/\\\\\\\\/g; s/\"/\\\\\"/g'); printf 'window.NOVA_SRE_API_BASE = \"%s\";\\n' \"$escaped\" > /usr/share/nginx/html/config.js; nginx -g 'daemon off;'"]
