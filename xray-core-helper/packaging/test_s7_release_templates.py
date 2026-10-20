"""Guard the selected Xray template and its reviewed binding contract.

These tests describe the post-activation state: the reviewed draft has become
``templConf/xray/config.json`` and ``config.json.tmpl`` must not remain a build,
install or bundle dependency.
"""
import re
from pathlib import Path
import unittest

PACKAGING = Path(__file__).resolve().parent
HELPER = PACKAGING.parent
XRAY_TEMPLATES = HELPER / 'templConf' / 'xray'
CANONICAL = XRAY_TEMPLATES / 'config.json'
DRAFT = XRAY_TEMPLATES / 'config.json.tmpl'

EXPECTED_PLACEHOLDERS = {
    'access_log_path': 1,
    'error_log_path': 1,
    'log_level': 1,
    'dns_log': 1,
    'xray_api': 1,
    'user_geosite_reference': 2,
    'tproxy_port': 1,
    'lan_address': 1,
    'egress_mark': 3,
    'user_geoip_reference': 1,
}

EXPECTED_BINDINGS = {
    'access-log': ('log-destination', 'helper.logging.access-path'),
    'error-log': ('log-destination', 'helper.logging.error-path'),
    'log-level': ('log-level', 'helper.logging.level'),
    'dns-log': ('boolean', 'helper.logging.dns-log'),
    'api-listen': ('loopback-endpoint', 'helper.xray-api'),
    'dns-user-geosite': ('geosite-reference', 'helper.user-geosite'),
    'tproxy-port': ('port', 'xray.tproxy-port'),
    'dns-listen': ('ipv4-address', 'helper.network.address'),
    'egress-direct': ('uint32', 'xray.egress-mark'),
    'egress-proxy': ('uint32', 'xray.egress-mark'),
    'egress-dns-out': ('uint32', 'xray.egress-mark'),
    'routing-user-geosite': ('geosite-reference', 'helper.user-geosite'),
    'user-geoip': ('geoip-reference', 'helper.user-geoip'),
}


class XrayTemplateSelectionTest(unittest.TestCase):
    def test_canonical_xray_template_is_the_only_template_in_source_tree(self):
        self.assertTrue(CANONICAL.is_file(), f'missing selected template: {CANONICAL}')
        self.assertFalse(CANONICAL.is_symlink(), 'canonical Xray template must be a regular file')
        names = sorted(path.name for path in XRAY_TEMPLATES.iterdir())
        self.assertEqual(names, ['config.json'],
                         'xray template directory must contain exactly the selected template')

    def test_install_and_bundle_select_the_canonical_xray_template(self):
        installer = (PACKAGING / 'install-templconf.sh').read_text()
        selected = [line for line in installer.splitlines()
                    if line.startswith('install ') and 'templConf/xray/' in line]
        self.assertEqual(len(selected), 1,
                         'install-templconf.sh must install exactly one Xray template')
        self.assertIn('"$project_dir/templConf/xray/config.json"', selected[0])
        self.assertIn('"$factory_root/xray/config.json"', selected[0])

        bundle = (PACKAGING / 'install-bundle.sh').read_text()
        self.assertIn('"$helper_root/packaging/install-templconf.sh"', bundle,
                      'source bundle install must use the canonical template installer')

        tmpfiles = (PACKAGING / 'tmpfiles.d/xray-core-helper.conf').read_text()
        expected = ('C /etc/xray-core-helper/templConf/xray/config.json '
                    '0600 root root - '
                    '/usr/share/xray-core-helper/templConf/xray/config.json')
        self.assertIn(expected, tmpfiles,
                      'tmpfiles must seed the working copy from the factory Xray template')

    def test_canonical_xray_template_keeps_placeholders_and_binding_annotations(self):
        self.assertTrue(CANONICAL.is_file(), f'missing selected template: {CANONICAL}')
        text = CANONICAL.read_text(encoding='utf-8')

        placeholders = {}
        for name in re.findall(r'\$\{([A-Za-z_][A-Za-z0-9_]*)\}', text):
            placeholders[name] = placeholders.get(name, 0) + 1
        self.assertEqual(placeholders, EXPECTED_PLACEHOLDERS,
                         'selected template lost or invented a placeholder')

        annotations = re.findall(
            r'//\s*helper:v1\s+bind\s+(\S+)\s+type=(\S+)\s+source=(\S+)', text)
        ids = [annotation[0] for annotation in annotations]
        self.assertEqual(len(ids), len(set(ids)), 'binding annotation IDs must be unique')
        self.assertEqual({identifier: (kind, source) for identifier, kind, source in annotations},
                         EXPECTED_BINDINGS,
                         'selected template lost or changed a binding annotation')

    def test_draft_template_is_not_shipped_or_referenced(self):
        self.assertFalse(DRAFT.exists(),
                         'review draft must not remain after the canonical template is selected')
        for name in ('install-templconf.sh', 'install-bundle.sh', 'install-from-github.sh',
                     'build-release.py', 'verify-release.py', 'tmpfiles.d/xray-core-helper.conf'):
            path = PACKAGING / name
            self.assertNotIn('config.json.tmpl', path.read_text(),
                             f'{name} must not depend on the review draft')


if __name__ == '__main__':
    unittest.main()
