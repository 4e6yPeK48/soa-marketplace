"""Configuration regression checks; no Docker or HTTP calls."""
import unittest
from unittest.mock import patch
import e2e


class ConfigurationTests(unittest.TestCase):
    def test_compose_effective_values(self):
        config = {'services': {'catalog': {'ports': [{'target': 8080, 'published': '9088'}],
                  'environment': {'ADMIN_EMAIL': 'custom@example.test',
                                  'ADMIN_PASSWORD': 'custom-secret', 'ORDER_COOLDOWN_MINUTES': '7'}}}}
        values = e2e.configuration(config, {})
        self.assertEqual(values, ('http://127.0.0.1:9088', 'custom@example.test', 'custom-secret', 7))

    def test_base_override_and_defaults(self):
        config = {'services': {'catalog': {'ports': [{'target': 8080, 'published': '9999'}],
                                          'environment': {}}}}
        self.assertEqual(e2e.configuration(config, {'BASE_URL': 'http://example.test:7777/'}),
                         ('http://example.test:7777', 'admin@example.test', 'admin-local-password', 1))

    def test_age_uses_configured_cooldown_and_fixture_scope(self):
        with patch.object(e2e, 'COOLDOWN_MINUTES', 7, create=True), patch.object(e2e, 'sql') as sql:
            with patch.dict(e2e.FIXTURES, {'users': [42]}):
                e2e.age({'id': 42})
                self.assertIn("interval '8 minutes'", sql.call_args.args[0])
                self.assertIn('WHERE user_id=42', sql.call_args.args[0])
                with self.assertRaises(AssertionError):
                    e2e.age({'id': 999})


if __name__ == '__main__':
    unittest.main()
