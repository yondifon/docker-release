# Template: `check-upgrade.py` replaces {{VERSION}} and {{HEALTHCHECK}} before
# building, because ARG values are not expanded in CMD and HEALTHCHECK.
FROM busybox
ENV APP_VERSION={{VERSION}} SHARED_KEY=image-{{VERSION}}
LABEL app.version={{VERSION}}
EXPOSE 80
{{HEALTHCHECK}}
CMD ["sh", "-c", "mkdir -p /www && echo \"image=$APP_VERSION compose=$FROM_COMPOSE data=$(cat /data/marker 2>/dev/null) bind=$(cat /bind/marker 2>/dev/null) host=$(hostname) cmd={{VERSION}}\" > /www/index.html && exec httpd -f -p 80 -h /www"]
