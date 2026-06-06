# Content-routing example (UC-RTG-04). Gate content rules on parse_ok so an
# unparseable body falls back to source routing rather than misrouting.
#
#   starlark-test tools/testdata/content-routing.star \
#     --samples tools/testdata/content-routing-samples.yaml \
#     --expect  tools/testdata/content-routing-expected.yaml
def route(req):
    if req.parse_ok and req.query_category == "WRITE":
        return "etl"
    if req.parse_ok and "hive" in req.catalogs:
        return "warehouse"
    if req.source == "airflow":
        return "etl"
    return None
