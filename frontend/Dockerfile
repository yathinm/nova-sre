FROM node:22-alpine AS build

WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM nginx:1.27-alpine

COPY nginx.conf /etc/nginx/nginx.conf
COPY --from=build /app/dist /opt/nova-sre/html

RUN mkdir -p /tmp/nova-sre-html /tmp/nginx/client_body /tmp/nginx/proxy /tmp/nginx/fastcgi /tmp/nginx/uwsgi /tmp/nginx/scgi \
  && chown -R nginx:nginx /opt/nova-sre/html /tmp/nova-sre-html /tmp/nginx

EXPOSE 8080

USER nginx

CMD ["/bin/sh", "-ec", "mkdir -p /tmp/nova-sre-html /tmp/nginx/client_body /tmp/nginx/proxy /tmp/nginx/fastcgi /tmp/nginx/uwsgi /tmp/nginx/scgi; rm -rf /tmp/nova-sre-html/*; cp -R /opt/nova-sre/html/. /tmp/nova-sre-html/; escaped=$(printf '%s' \"${NOVA_SRE_API_BASE:-}\" | sed 's/\\\\/\\\\\\\\/g; s/\"/\\\\\"/g'); printf 'window.NOVA_SRE_API_BASE = \"%s\";\\n' \"$escaped\" > /tmp/nova-sre-html/config.js; nginx -g 'daemon off;'"]
