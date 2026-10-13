#!/usr/bin/env python3
"""Render the isolated Grafana deployment; never reads a Secret or talks to Kubernetes."""
import argparse
from copy import deepcopy
import hashlib
import json
import re
import sys
from urllib.parse import urlsplit
import yaml
from common import ROOT, resource
import certificates
import network
import provisioning
import proxy
import workloads

IMAGE = re.compile(r'harbor\.wellspiking\.ai/guofeng\.su/grafana-observability:13\.1\.1-clickhouse-4\.22\.1[-a-z0-9]*@sha256:[a-f0-9]{64}')


def validate(values):
    expected = {'namespace', 'replicas', 'image', 'rootURL', 'allowedOrigins', 'prometheusURL', 'lokiURL',
                'postgresSecret', 'adminSecret', 'vmTokenSecret', 'clickhouseSecret'}
    if set(values) != expected or values['namespace'] != 'grafana' or values['replicas'] != 2:
        raise ValueError('expected exact Grafana namespace, two replicas and known configuration keys')
    if not IMAGE.fullmatch(values['image']):
        raise ValueError('supply a reviewed Grafana 13.1.1 / ClickHouse 4.22.1 derived image digest')
    for field in ('rootURL', 'prometheusURL', 'lokiURL'):
        value = values[field]
        parsed = urlsplit(value)
        if not re.fullmatch(r'https?://[a-zA-Z0-9.:-]+/?', value) or parsed.username or parsed.password:
            raise ValueError('unsafe datasource or browser URL')
    if not values['rootURL'].startswith('https://'):
        raise ValueError('browser URL must use HTTPS')
    if (not isinstance(values['allowedOrigins'], list) or not values['allowedOrigins'] or
            any(not re.fullmatch(r'https://[a-zA-Z0-9.:-]+', origin) for origin in values['allowedOrigins'])):
        raise ValueError('expected explicit HTTPS origins')
    for field in ('postgresSecret', 'adminSecret', 'vmTokenSecret', 'clickhouseSecret'):
        if not re.fullmatch(r'[a-z0-9][a-z0-9-]{0,62}', values[field]):
            raise ValueError('unsafe external Secret reference')


def bootstrap():
    namespace = resource('Namespace', 'grafana', namespaced=False)
    namespace['metadata']['labels'] = {**namespace['metadata']['labels'],
        'pod-security.kubernetes.io/enforce': 'restricted'}
    quota = resource('ResourceQuota', 'grafana-budget', {'spec': {'hard': {
        'requests.cpu': '4', 'requests.memory': '8Gi', 'limits.cpu': '16', 'limits.memory': '16Gi',
        'requests.storage': '60Gi', 'persistentvolumeclaims': '3', 'pods': '12', 'requests.nvidia.com/gpu': '0'}}})
    return [namespace, quota]


def render(values):
    validate(values)
    values = deepcopy(values)
    configs = [*provisioning.render(values), proxy.render()]
    checksum = hashlib.sha256(json.dumps(configs, sort_keys=True).encode()).hexdigest()
    applications = []
    for doc in workloads.render(values):
        if doc['kind'] == 'Deployment':
            template = doc['spec']['template']
            template = {**template, 'metadata': {**template['metadata'], 'annotations': {
                'checksum/grafana-config': checksum}}}
            doc = {**doc, 'spec': {**doc['spec'], 'template': template}}
        applications.append(doc)
    return [*bootstrap(), *certificates.render(), *configs, *applications, *network.render()]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--values', default=ROOT / 'values.yaml')
    parser.add_argument('--image', help='builder-produced derived image with immutable registry digest')
    parser.add_argument('--part', choices=('all', 'bootstrap', 'certificates', 'application'), default='all')
    args = parser.parse_args()
    values = yaml.safe_load(open(args.values))
    values = {**values, **({'image': args.image} if args.image else {})}
    if args.part == 'certificates':
        docs = certificates.render()
    elif args.part == 'bootstrap':
        # Bootstrap is independent of the application image, but remains exactly scoped.
        docs = bootstrap()
    else:
        docs = render(values)
        if args.part == 'application':
            excluded = {'Certificate', 'CertificateRequestPolicy', 'ClusterIssuer', 'ClusterRole', 'ClusterRoleBinding', 'Namespace'}
            docs = [doc for doc in docs if doc['kind'] not in excluded]
    yaml.safe_dump_all(docs, sys.stdout, sort_keys=False)


if __name__ == '__main__':
    main()
