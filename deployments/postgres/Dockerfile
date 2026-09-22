FROM postgis/postgis:17-3.5

# postgis 官方镜像不含 pgRouting，从 PGDG 源补装
RUN apt-get update \
 && apt-get install -y --no-install-recommends postgresql-17-pgrouting \
 && rm -rf /var/lib/apt/lists/*
