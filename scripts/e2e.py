#!/usr/bin/env python3
"""Black-box HW2 acceptance. Keeps uniquely named fixtures for SQL demonstration."""
import base64
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timedelta, timezone
from decimal import Decimal
import json
import os
import re
from pathlib import Path
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]
BASE = 'http://127.0.0.1:8080'
COOLDOWN_MINUTES = 1
CONTEXT = os.getenv('DOCKER_CONTEXT', 'colima-distsys')
RUN = uuid.uuid4().hex[:10].upper()
PASSWORD = 'E2e-local-password'
FIXTURES = {'run': RUN, 'users': [], 'products': [], 'orders': [], 'promos': []}
SECRETS = [PASSWORD]
REQUESTS = []


def configuration(compose, environ):
    catalog = compose['services']['catalog']
    environment = catalog.get('environment', {})
    port = next(str(p['published']) for p in catalog['ports'] if int(p['target']) == 8080)
    return (environ.get('BASE_URL', 'http://127.0.0.1:' + port).rstrip('/'),
            environment.get('ADMIN_EMAIL', 'admin@example.test'),
            environment.get('ADMIN_PASSWORD', 'admin-local-password'),
            int(environment.get('ORDER_COOLDOWN_MINUTES', '1')))


def load_configuration():
    # Capture resolved Compose privately: its environment contains credentials.
    result = subprocess.run(['docker', '--context', CONTEXT, 'compose', 'config', '--format', 'json'],
                            cwd=ROOT, text=True, capture_output=True)
    if result.returncode:
        raise RuntimeError('Cannot resolve Compose configuration; check Docker context and .env')
    return configuration(json.loads(result.stdout), os.environ)


def test_public_routes():
    for method, path in [('GET', '/'), ('HEAD', '/'), ('GET', '/health'), ('GET', '/openapi.yaml')]:
        req = urllib.request.Request(BASE + path, method=method)
        with urllib.request.urlopen(req, timeout=20) as response:
            assert response.status == 200
            rid = response.headers.get('X-Request-Id')
            assert rid and str(uuid.UUID(rid)) == rid.lower()
            REQUESTS.append(rid)
            content = response.read()
            if method == 'HEAD':
                assert not content
            elif path == '/':
                assert 'text/html' in response.headers.get('Content-Type', '')
                assert b'<html' in content.lower() or b'<!doctype html' in content.lower()
            elif path == '/health':
                assert content == b'ok\n'
                assert 'text/plain' in response.headers.get('Content-Type', '')
            else:
                assert b'openapi:' in content
                assert 'attachment' in response.headers.get('Content-Disposition', '')
    for token in (None, 'malformed'):
        for path in ('/missing', '/auth/nope', '/products/1/nope', '/swagger/missing', '/swagger/missing.js'):
            request('GET', path, token=token, status=404, code='ROUTE_NOT_FOUND')
    req = urllib.request.Request(BASE + '/products', method='DELETE')
    try:
        urllib.request.urlopen(req, timeout=20)
        raise AssertionError('DELETE /products must return 405')
    except urllib.error.HTTPError as response:
        with response:
            assert response.status == 405
            assert {'GET', 'POST'} <= {m.strip() for m in response.headers.get('Allow', '').split(',')}
            assert json.loads(response.read())['error_code'] == 'METHOD_NOT_ALLOWED'
            rid = response.headers.get('X-Request-Id')
            assert rid and str(uuid.UUID(rid)) == rid.lower()
            REQUESTS.append(rid)
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, req, fp, code, msg, headers, newurl):
            return None

    try:
        urllib.request.build_opener(NoRedirect()).open(BASE + '/swagger', timeout=20)
        raise AssertionError('/swagger must redirect to /swagger/')
    except urllib.error.HTTPError as response:
        with response:
            assert response.status in (301, 308), ('/swagger redirect', response.status)
            assert response.headers['Location'] == '/swagger/'
            rid = response.headers.get('X-Request-Id')
            assert rid and str(uuid.UUID(rid)) == rid.lower()
            REQUESTS.append(rid)
    for path, content_type in [('/swagger/', 'text/html'),
                               ('/swagger/swagger-ui.css', 'text/css'),
                               ('/swagger/swagger-ui-bundle.js', 'javascript')]:
        for token in (None, 'malformed'):
            headers = {'Authorization': 'Bearer ' + token} if token else {}
            req = urllib.request.Request(BASE + path, headers=headers)
            with urllib.request.urlopen(req, timeout=20) as response:
                assert response.status == 200
                assert content_type in response.headers.get('Content-Type', '')
                content = response.read()
                assert content
                rid = response.headers.get('X-Request-Id')
                assert rid and str(uuid.UUID(rid)) == rid.lower()
                REQUESTS.append(rid)
                if path == '/swagger/':
                    html = content.decode()
                    assert 'SwaggerUIBundle' in html and '/openapi.yaml' in html
                    assert re.search(r'validatorUrl\s*:\s*null', html)
                    assert re.search(r'queryConfigEnabled\s*:\s*false', html)
                    urls = re.findall(r'(?:src|href)=["\']([^"\']+)["\']', html)
                    assert any(url.endswith('swagger-ui.css') for url in urls)
                    assert any(url.endswith('swagger-ui-bundle.js') for url in urls)
                    assert all(not urllib.parse.urlsplit(url).scheme and not url.startswith('//') for url in urls)
    req = urllib.request.Request(BASE + '/swagger/', method='POST')
    try:
        urllib.request.urlopen(req, timeout=20)
        raise AssertionError('POST /swagger/ must return 405')
    except urllib.error.HTTPError as response:
        with response:
            assert response.status == 405
            assert 'GET' in {m.strip() for m in response.headers.get('Allow', '').split(',')}
            assert json.loads(response.read())['error_code'] == 'METHOD_NOT_ALLOWED'
            rid = response.headers.get('X-Request-Id')
            assert rid and str(uuid.UUID(rid)) == rid.lower()
            REQUESTS.append(rid)
    request('GET', '/products/1', status=401, code='TOKEN_INVALID')


def request(method, path, body=None, token=None, status=200, code=None, raw=None):
    data = raw if raw is not None else (json.dumps(body).encode() if body is not None else None)
    headers = {'Content-Type': 'application/json'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    req = urllib.request.Request(BASE + path, data=data, headers=headers, method=method)
    try:
        response = urllib.request.urlopen(req, timeout=20)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        content = response.read()
        value = json.loads(content, parse_float=Decimal) if content else None
        rid = response.headers.get('X-Request-Id')
        assert rid and str(uuid.UUID(rid)) == rid.lower(), ('missing/invalid request UUID', method, path)
        REQUESTS.append(rid)
        assert response.status == status, (method, path, response.status, status, value)
        if code:
            assert value['error_code'] == code and value['message'], (path, code, value)
            if code == 'ORDER_LIMIT_EXCEEDED':
                seconds = value['details']['retry_after_seconds']
                assert seconds >= 1 and int(response.headers['Retry-After']) == seconds
            if code == 'INVALID_STATE_TRANSITION':
                assert {'current_status', 'requested_status', 'allowed_statuses'} <= value['details'].keys()
        return value


def sql(query):
    result = subprocess.run(['docker', '--context', CONTEXT, 'compose', 'exec', '-T',
                             'postgres', 'psql', '-U', 'marketplace', '-d', 'marketplace',
                             '-At', '-v', 'ON_ERROR_STOP=1', '-c', query], cwd=ROOT,
                            text=True, capture_output=True, check=True)
    return result.stdout.strip()


def user(role='USER'):
    email = f'e2e-{RUN}-{len(FIXTURES["users"])}@example.test'
    out = request('POST', '/auth/register', {'email': email, 'password': PASSWORD, 'role': role}, status=201)
    FIXTURES['users'].append(out['id'])
    pair = request('POST', '/auth/login', {'email': email, 'password': PASSWORD})
    SECRETS.extend([pair['access_token'], pair['refresh_token']])
    return {'id': out['id'], 'token': pair['access_token'], 'pair': pair, 'email': email}


def product(token, **changes):
    body = {'name': f'E2E-{RUN}-{len(FIXTURES["products"])}', 'description': None,
            'price': 100, 'stock': 20, 'category': RUN, 'status': 'ACTIVE'}
    body.update(changes)
    out = request('POST', '/products', body, token, 201)
    FIXTURES['products'].append(out['id'])
    return out


def replace_product(p, **changes):
    body = {key: p[key] for key in ('name', 'description', 'price', 'stock', 'category', 'status') if key in p}
    body['price'] = float(body['price'])
    body.update(changes)
    return request('PUT', f'/products/{p["id"]}', body, SELLER['token'])


def items(p, quantity=1):
    return {'items': [{'product_id': p['id'], 'quantity': quantity}]}


def order(u, p, quantity=1, promo=None):
    body = items(p, quantity)
    if promo:
        body['promo_code'] = promo['code']
    out = request('POST', '/orders', body, u['token'], 201)
    FIXTURES['orders'].append(out['id'])
    return out


def promo(**changes):
    now = datetime.now(timezone.utc)
    body = {'code': f'P{RUN}{len(FIXTURES["promos"])}', 'discount_type': 'PERCENTAGE',
            'discount_value': 20, 'min_order_amount': 0, 'max_uses': 10,
            'valid_from': (now - timedelta(days=1)).isoformat(),
            'valid_until': (now + timedelta(days=1)).isoformat(), 'active': True}
    body.update(changes)
    out = request('POST', '/promo-codes', body, SELLER['token'], 201)
    FIXTURES['promos'].append(out['id'])
    return out


def age(u):
    assert u['id'] in FIXTURES['users']
    sql(f"UPDATE user_operations SET created_at=NOW()-interval '{COOLDOWN_MINUTES + 1} minutes' WHERE user_id={u['id']}")


def stock(p):
    return int(sql(f'SELECT stock FROM products WHERE id={p["id"]}'))


def uses(p):
    return int(sql(f'SELECT current_uses FROM promo_codes WHERE id={p["id"]}'))


def cancel(u, o):
    return request('POST', f'/orders/{o["id"]}/cancel', token=u['token'])


def snapshot(u, p, o):
    return (stock(p), sql(f'SELECT row_to_json(o) FROM orders o WHERE id={o["id"]}'),
            sql(f'SELECT row_to_json(i) FROM order_items i WHERE order_id={o["id"]} ORDER BY id'),
            sql(f'SELECT count(*) FROM user_operations WHERE user_id={u["id"]}'))


def test_auth():
    request('GET', '/products', status=401, code='TOKEN_INVALID')
    request('GET', '/products', token='malformed', status=401, code='TOKEN_INVALID')
    request('POST', '/auth/register', {'email': f'admin-{RUN}@example.test', 'password': PASSWORD,
                                     'role': 'ADMIN'}, status=400, code='VALIDATION_ERROR')
    request('POST', '/auth/register', {'email': BUYER['email'], 'password': PASSWORD,
                                     'role': 'USER'}, status=409, code='EMAIL_ALREADY_EXISTS')
    pair = BUYER['pair']
    refreshed = request('POST', '/auth/refresh', {'refresh_token': pair['refresh_token']})
    SECRETS.extend([refreshed['access_token'], refreshed['refresh_token']])
    request('GET', '/products', token=refreshed['access_token'])
    request('GET', '/products', token=pair['refresh_token'], status=401, code='TOKEN_INVALID')
    request('POST', '/auth/refresh', {'refresh_token': pair['access_token']}, status=401,
            code='REFRESH_TOKEN_INVALID')
    request('POST', '/auth/refresh', {'refresh_token': 'bad'}, status=401, code='REFRESH_TOKEN_INVALID')
    claims = json.loads(base64.urlsafe_b64decode(pair['access_token'].split('.')[1] + '=='))
    assert claims['role'] == 'USER' and pair['expires_in'] in range(900, 1801)


def test_crud_roles():
    p = product(SELLER['token'])
    other = user('SELLER')
    body = {'name': 'Denied', 'price': 1, 'stock': 1, 'category': RUN, 'status': 'ACTIVE'}
    request('POST', '/products', body, BUYER['token'], 403, 'ACCESS_DENIED')
    admin_product = product(ADMIN)
    for method, path in [('POST', '/products'), ('PUT', f'/products/{admin_product["id"]}')]:
        out = request(method, path, {**body, 'seller_id': 9223372036854775807},
                      ADMIN, 400, 'VALIDATION_ERROR')
        assert out['details'].get('seller_id')
    assert request('GET', f'/products/{admin_product["id"]}', token=ADMIN)['stock'] == 20
    for method in ('PUT', 'DELETE'):
        request(method, f'/products/{p["id"]}', body if method == 'PUT' else None,
                other['token'], 403, 'ACCESS_DENIED')
        request(method, f'/products/{p["id"]}', body if method == 'PUT' else None,
                BUYER['token'], 403, 'ACCESS_DENIED')
    replace_product(p, name='Updated-' + RUN, status='INACTIVE')
    page = request('GET', f'/products?category={RUN}&status=INACTIVE&page=0&size=1', token=BUYER['token'])
    assert page['totalElements'] == 1 and page['page'] == 0 and page['size'] == 1
    assert page['items'][0]['id'] == p['id']
    assert request('GET', f'/products?category={RUN}&status=INACTIVE&page=1&size=1', token=BUYER['token'])['items'] == []
    assert request('GET', '/products', token=SELLER['token'])['size'] == 20
    request('DELETE', f'/products/{p["id"]}', token=ADMIN, status=204)
    assert sql(f"SELECT status FROM products WHERE id={p['id']}") == 'ARCHIVED'
    assert request('GET', f'/products/{p["id"]}', token=BUYER['token'])['status'] == 'ARCHIVED'
    request('GET', '/products/9223372036854775807', token=BUYER['token'], status=404, code='PRODUCT_NOT_FOUND')
    request('POST', '/orders', items(p), SELLER['token'], 403, 'ACCESS_DENIED')
    now = datetime.now(timezone.utc)
    request('POST', '/promo-codes', {'code': 'DENIED_' + RUN, 'discount_type': 'PERCENTAGE',
            'discount_value': 10, 'min_order_amount': 0, 'max_uses': 1,
            'valid_from': (now - timedelta(days=1)).isoformat(),
            'valid_until': (now + timedelta(days=1)).isoformat()}, BUYER['token'], 403, 'ACCESS_DENIED')


def test_validation():
    body = {'name': 'valid', 'price': 1, 'stock': 1, 'category': RUN, 'status': 'ACTIVE'}
    invalid = [{}, {**body, 'name': ''}, {**body, 'name': 'x' * 256},
               {**body, 'description': 'x' * 4001}, {**body, 'price': 0}, {**body, 'price': 1.001},
               {**body, 'stock': -1}, {**body, 'category': ''},
               {**body, 'category': 'x' * 101}, {**body, 'status': 'BAD'}, {**body, 'name': None}]
    for data in invalid:
        out = request('POST', '/products', data, SELLER['token'], 400, 'VALIDATION_ERROR')
        assert out.get('details'), ('missing field violations', data)
    for query in ('page=-1', 'size=0', 'size=101', 'status=BAD'):
        request('GET', '/products?' + query, token=BUYER['token'], status=400, code='VALIDATION_ERROR')
    bad_items = [{}, {'items': []}, {'items': None}, {'items': [{'product_id': 1, 'quantity': 0}]},
                 {'items': [{'product_id': 1, 'quantity': 1000}]},
                 {'items': [{'product_id': 1, 'quantity': 1}] * 51},
                 {'items': [{'product_id': 1, 'quantity': 1}], 'promo_code': 'bad!'}]
    duplicate_product = product(SELLER['token'])
    bad_items.append({'items': [{'product_id': duplicate_product['id'], 'quantity': 1}] * 2})
    for data in bad_items:
        assert request('POST', '/orders', data, BUYER['token'], 400, 'VALIDATION_ERROR').get('details')
    assert stock(duplicate_product) == 20
    assert sql(f'SELECT count(*) FROM user_operations WHERE user_id={BUYER["id"]}') == '0'
    assert request('POST', '/products', token=SELLER['token'], raw=b'{', status=400,
                   code='VALIDATION_ERROR').get('details')


def test_order_lifecycle():
    u, stranger = user(), user()
    p, discount = product(SELLER['token']), promo(max_uses=1)
    o = order(u, p, 2, discount)
    assert stock(p) == 18 and uses(discount) == 1
    assert Decimal(o['total_amount']) == 160 and Decimal(o['discount_amount']) == 40
    assert sql(f"SELECT operation_type FROM user_operations WHERE user_id={u['id']}") == 'CREATE_ORDER'
    assert sql(f"SELECT u.created_at > o.created_at FROM user_operations u JOIN orders o ON o.user_id=u.user_id WHERE o.id={o['id']} AND u.operation_type='CREATE_ORDER'") == 't', 'create cooldown starts before operation is recorded'
    request('POST', '/orders', items(p), u['token'], 429, 'ORDER_LIMIT_EXCEEDED')
    age(u)
    request('POST', '/orders', items(p), u['token'], 409, 'ORDER_HAS_ACTIVE')
    for method, path, body in [('GET', f'/orders/{o["id"]}', None),
                               ('PUT', f'/orders/{o["id"]}', items(p)),
                               ('POST', f'/orders/{o["id"]}/cancel', None)]:
        request(method, path, body, stranger['token'], 403, 'ORDER_OWNERSHIP_VIOLATION')
        request(method, path, body, SELLER['token'], 403, 'ACCESS_DENIED')
    request('GET', f'/orders/{o["id"]}', token=ADMIN)
    replace_product(p, price=125, stock=18)
    assert request('GET', f'/orders/{o["id"]}', token=u['token'])['items'][0]['price_at_order'] == 100
    updated = request('PUT', f'/orders/{o["id"]}', items(p, 3), u['token'])
    assert stock(p) == 17 and uses(discount) == 1
    assert updated['items'][0]['price_at_order'] == 125 and updated['total_amount'] == 300
    request('PUT', f'/orders/{o["id"]}', items(p), u['token'], 429, 'ORDER_LIMIT_EXCEEDED')
    assert sql(f"SELECT count(*) FROM user_operations WHERE user_id={u['id']} AND operation_type='UPDATE_ORDER'") == '1'
    assert sql(f"SELECT u.created_at >= o.updated_at FROM user_operations u JOIN orders o ON o.user_id=u.user_id WHERE o.id={o['id']} AND u.operation_type='UPDATE_ORDER'") == 't', 'update cooldown starts before order is saved'
    assert cancel(u, o)['status'] == 'CANCELED' and stock(p) == 20 and uses(discount) == 0
    request('POST', f'/orders/{o["id"]}/cancel', token=u['token'], status=409, code='INVALID_STATE_TRANSITION')
    request('GET', '/orders/9223372036854775807', token=u['token'], status=404, code='ORDER_NOT_FOUND')


def test_failures_rollback():
    u, p = user(), product(SELLER['token'])
    inactive = product(SELLER['token'], status='INACTIVE')
    for candidate, qty, status, code in [(inactive, 1, 409, 'PRODUCT_INACTIVE'),
                                         ({'id': 9223372036854775807}, 1, 404, 'PRODUCT_NOT_FOUND'),
                                         (p, 21, 409, 'INSUFFICIENT_STOCK')]:
        out = request('POST', '/orders', items(candidate, qty), u['token'], status, code)
        if code == 'INSUFFICIENT_STOCK':
            assert out.get('details')
        assert stock(p) == 20
    for discount, code in [(promo(active=False), 'PROMO_CODE_INVALID'),
                            (promo(min_order_amount=101), 'PROMO_CODE_MIN_AMOUNT')]:
        request('POST', '/orders', {**items(p), 'promo_code': discount['code']}, u['token'], 422, code)
        assert stock(p) == 20 and uses(discount) == 0
    request('POST', '/orders', {**items(p), 'promo_code': 'UNKNOWN_123'}, u['token'], 422, 'PROMO_CODE_INVALID')
    expensive = product(SELLER['token'], price=9999999999.99, stock=2)
    out = request('POST', '/orders', items(expensive, 2), u['token'], 400, 'VALIDATION_ERROR')
    assert out['details'].get('items') and stock(expensive) == 2
    assert sql(f'SELECT count(*) FROM user_operations WHERE user_id={u["id"]}') == '0'
    assert sql(f'SELECT count(*) FROM orders WHERE user_id={u["id"]}') == '0'
    o = order(u, p, 2)
    before = snapshot(u, p, o)
    request('PUT', f'/orders/{o["id"]}', items(p, 21), u['token'], 409, 'INSUFFICIENT_STOCK')
    assert snapshot(u, p, o) == before, 'failed update leaked stock/order/items/operation'
    cancel(u, o)


def test_money_stock_boundaries():
    u, p = user(), product(SELLER['token'], price=0.29)
    o = order(u, p, 3)
    assert o['total_amount'] == Decimal('0.87')
    assert o['items'][0]['price_at_order'] == Decimal('0.29')
    assert sql(f'SELECT total_amount FROM orders WHERE id={o["id"]}') == '0.87'
    cancel(u, o)
    u, p = user(), product(SELLER['token'], stock=1)
    o = order(u, p)
    assert stock(p) == 0
    replace_product(p, stock=2147483647)
    cancel(u, o)
    assert stock(p) == 2147483648
    assert request('GET', f'/products/{p["id"]}', token=u['token'])['stock'] == 2147483648


def test_discounted_order_amounts():
    u, p, d = user(), product(SELLER['token'], price=6_000_000_000), promo(discount_value=50)
    o = order(u, p, 2, d)
    assert o['total_amount'] == o['discount_amount'] == 6_000_000_000
    assert stock(p) == 18 and uses(d) == 1
    assert sql(f'SELECT total_amount,discount_amount FROM orders WHERE id={o["id"]}') == '6000000000.00|6000000000.00'
    updated = request('PUT', f'/orders/{o["id"]}', items(p, 3), u['token'])
    assert updated['total_amount'] == updated['discount_amount'] == 9_000_000_000
    assert stock(p) == 17 and uses(d) == 1
    assert sql(f'SELECT total_amount,discount_amount FROM orders WHERE id={o["id"]}') == '9000000000.00|9000000000.00'
    cancel(u, o)
    assert stock(p) == 20 and uses(d) == 0

    for percentage, quantity in [(10, 2), (70, 3)]:
        u, d = user(), promo(discount_value=percentage)
        request('POST', '/orders', {**items(p, quantity), 'promo_code': d['code']},
                u['token'], 400, 'VALIDATION_ERROR')
        assert stock(p) == 20 and uses(d) == 0
        assert sql(f'SELECT count(*) FROM orders WHERE user_id={u["id"]}') == '0'
        assert sql(f'SELECT count(*) FROM user_operations WHERE user_id={u["id"]}') == '0'
        o = order(u, p, promo=d)
        before = snapshot(u, p, o)
        request('PUT', f'/orders/{o["id"]}', items(p, quantity), u['token'], 400, 'VALIDATION_ERROR')
        assert snapshot(u, p, o) == before, 'amount overflow leaked stock/order/items/operation'
        assert uses(d) == 1
        cancel(u, o)
        assert stock(p) == 20 and uses(d) == 0


def test_promos_states():
    for value, kind, expected in [(70, 'PERCENTAGE', 30), (71, 'PERCENTAGE', 100),
                                   (200, 'FIXED_AMOUNT', 0)]:
        u, p = user(), product(SELLER['token'])
        d = promo(discount_value=value, discount_type=kind)
        o = order(u, p, promo=d)
        assert o['total_amount'] == expected
        cancel(u, o)
    u, p, d = user(), product(SELLER['token']), promo(min_order_amount=150)
    o = order(u, p, 2, d)
    updated = request('PUT', f'/orders/{o["id"]}', items(p), u['token'])
    assert updated['promo_code_id'] is None and updated['discount_amount'] == 0 and uses(d) == 0
    cancel(u, o)
    for mutation in ("active=false", "valid_until=NOW()-interval '1 second'"):
        u, p, d = user(), product(SELLER['token']), promo()
        o = order(u, p, promo=d)
        sql(f'UPDATE promo_codes SET {mutation} WHERE id={d["id"]}')
        before = snapshot(u, p, o)
        request('PUT', f'/orders/{o["id"]}', items(p, 2), u['token'], 422, 'PROMO_CODE_INVALID')
        assert snapshot(u, p, o) == before and uses(d) == 1
        cancel(u, o)
    u, p = user(), product(SELLER['token'])
    o = order(u, p)
    request('POST', f'/orders/{o["id"]}/status', {'status': 'PAID'}, ADMIN, 409, 'INVALID_STATE_TRANSITION')
    request('POST', f'/orders/{o["id"]}/status', {'status': 'PAYMENT_PENDING'}, u['token'], 403, 'ACCESS_DENIED')
    for state in ('PAYMENT_PENDING', 'PAID', 'SHIPPED', 'COMPLETED'):
        assert request('POST', f'/orders/{o["id"]}/status', {'status': state}, ADMIN)['status'] == state
        request('PUT', f'/orders/{o["id"]}', items(p), u['token'], 409, 'INVALID_STATE_TRANSITION')
        if state != 'PAYMENT_PENDING':
            request('POST', f'/orders/{o["id"]}/cancel', token=u['token'], status=409, code='INVALID_STATE_TRANSITION')
    assert stock(p) == 19
    u = user()
    o = order(u, p)
    request('POST', f'/orders/{o["id"]}/status', {'status': 'PAYMENT_PENDING'}, ADMIN)
    cancel(u, o)
    assert stock(p) == 19


def test_concurrency():
    def compete(users, p, discount=None, failure='INSUFFICIENT_STOCK', failure_status=409):
        def attempt(u):
            body = items(p)
            if discount:
                body['promo_code'] = discount['code']
            # Collect successes and business errors without assuming which worker wins.
            req = urllib.request.Request(BASE + '/orders', json.dumps(body).encode(),
                  {'Content-Type': 'application/json', 'Authorization': 'Bearer ' + u['token']}, method='POST')
            try:
                response = urllib.request.urlopen(req, timeout=20)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                return response.status, json.loads(response.read()), u
        with ThreadPoolExecutor(max_workers=2) as pool:
            results = list(pool.map(attempt, users))
        wins = [r for r in results if r[0] == 201]
        losses = [r for r in results if r[0] != 201]
        assert len(wins) == len(losses) == 1, results
        assert losses[0][0] == failure_status and losses[0][1]['error_code'] == failure, losses
        FIXTURES['orders'].append(wins[0][1]['id'])
        return wins[0]
    p = product(SELLER['token'], stock=1)
    win = compete([user(), user()], p)
    assert stock(p) == 0
    cancel(win[2], win[1])
    assert stock(p) == 1
    p, u = product(SELLER['token']), user()
    win = compete([u, u], p, failure='ORDER_LIMIT_EXCEEDED', failure_status=429)
    assert stock(p) == 19
    assert sql(f"SELECT count(*) FROM orders WHERE user_id={u['id']} AND status IN ('CREATED','PAYMENT_PENDING')") == '1'
    cancel(u, win[1])
    p, d = product(SELLER['token']), promo(max_uses=1)
    win = compete([user(), user()], p, d, 'PROMO_CODE_INVALID', 422)
    assert stock(p) == 19 and uses(d) == 1
    cancel(win[2], win[1])
    assert stock(p) == 20 and uses(d) == 0


def test_logs():
    output = subprocess.run(['docker', '--context', CONTEXT, 'compose', 'logs', '--no-color',
                             '--no-log-prefix', 'catalog'], cwd=ROOT, capture_output=True,
                            text=True, check=True).stdout
    for secret in SECRETS:
        assert secret not in output, 'sensitive data leaked into API logs'
    records = []
    for line in output.splitlines():
        try:
            entry = json.loads(line)
        except json.JSONDecodeError:
            continue
        if entry.get('request_id') in REQUESTS:
            records.append(entry)
    assert len(records) >= len(REQUESTS), 'not every request has a JSON log'
    for entry in records:
        for key in ('request_id', 'method', 'endpoint', 'status_code', 'duration_ms', 'user_id', 'timestamp'):
            assert key in entry, ('missing log field', key)
        datetime.fromisoformat(entry['timestamp'].replace('Z', '+00:00'))
        assert entry['duration_ms'] >= 0
        if entry['method'] in ('POST', 'PUT', 'DELETE'):
            assert 'request_body' in entry or 'body' in entry, 'mutation body missing from logs'


if __name__ == '__main__':
    try:
        BASE, ADMIN_EMAIL, ADMIN_PASSWORD, COOLDOWN_MINUTES = load_configuration()
        SECRETS.append(ADMIN_PASSWORD)
        test_public_routes()
        print('PASS test_public_routes', flush=True)
        SELLER, BUYER = user('SELLER'), user()
        ADMIN = request('POST', '/auth/login', {'email': ADMIN_EMAIL,
                        'password': ADMIN_PASSWORD})['access_token']
        SECRETS.append(ADMIN)
        for test in (test_auth, test_crud_roles, test_validation, test_order_lifecycle,
                     test_failures_rollback, test_money_stock_boundaries, test_discounted_order_amounts,
                     test_promos_states, test_concurrency, test_logs):
            test()
            print('PASS', test.__name__, flush=True)
        print('PASS full marketplace HW2 E2E; retained fixtures:', json.dumps(FIXTURES))
    except Exception as error:
        message = str(error)
        for secret in SECRETS:
            if secret:
                message = message.replace(secret, '[REDACTED]')
        if isinstance(error, urllib.error.URLError):
            message = 'Cannot reach marketplace; run make up and check BASE_URL / PORT'
        print('FAIL', type(error).__name__, message, file=sys.stderr)
        print('Retained fixtures:', json.dumps(FIXTURES), file=sys.stderr)
        sys.exit(1)
