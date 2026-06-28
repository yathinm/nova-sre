FROM nginx:1.27-alpine

COPY index.html /usr/share/nginx/html/index.html
COPY styles.css /usr/share/nginx/html/styles.css
COPY app.js /usr/share/nginx/html/app.js
COPY config.js /usr/share/nginx/html/config.js

EXPOSE 80

CMD ["/bin/sh", "-c", "escaped=$(printf '%s' \"${NOVA_SRE_API_BASE:-}\" | sed 's/\\\\/\\\\\\\\/g; s/\"/\\\\\"/g'); printf 'window.NOVA_SRE_API_BASE = \"%s\";\\n' \"$escaped\" > /usr/share/nginx/html/config.js; nginx -g 'daemon off;'"]
