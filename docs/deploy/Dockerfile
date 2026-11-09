# syntax=docker/dockerfile:1
FROM node:lts AS prod
ENV FORCE_COLOR=0
WORKDIR /opt/docs-rune

COPY package.json package-lock.json ./
RUN npm ci
COPY . .
# GA4 property baked into the static build. Defaults to staging via
# docusaurus.config.ts; cloudbuild passes the prod value for deploy-prod.
ARG GA_MEASUREMENT_ID=""
ENV GA_MEASUREMENT_ID=$GA_MEASUREMENT_ID
RUN npm run build

FROM nginx:stable-alpine AS nginx
COPY --from=prod /opt/docs-rune/deploy/nginx.conf /etc/nginx/templates/default.conf.template
COPY --from=prod /opt/docs-rune/build /usr/share/nginx/docs-rune
