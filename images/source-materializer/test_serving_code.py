import base64
import hashlib
import http.client
import importlib.util
import io
import os
import tempfile
import unittest
import urllib.request
from contextlib import redirect_stderr
from email.message import Message
from pathlib import Path
from unittest import mock
from urllib.response import addinfourl

spec = importlib.util.spec_from_file_location('serving_code', Path(__file__).with_name('platform-fetch-serving-code.py'))
fetch = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fetch)

CONTENT = b'zip-code-test-fixture'
BASE = 'https://backend.platform.svc:8080/api/v1/internal'
JOB = 'job-' + '1' * 24

class Transport(urllib.request.HTTPSHandler):
    def __init__(self, body=CONTENT, status=200, headers=None):
        super().__init__()
        self.body,self.status,self.headers=body,status,headers or {}
        self.requests=[]
    def https_open(self,request):
        self.requests.append(request)
        headers=Message()
        for key,value in self.headers.items():
            headers[key]=value
        response=addinfourl(io.BytesIO(self.body),headers,request.full_url,self.status)
        response.msg='fixture'
        return response

class ServingCodeTest(unittest.TestCase):
    def setUp(self):
        self.directory=tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root=Path(self.directory.name)
        self.auth_file=self.root/'job-credential'
        self.auth_file.write_bytes(base64.urlsafe_b64encode(bytes(range(32))).rstrip(b'='))
        self.output=self.root/'serving.zip'
        self.digest=hashlib.sha256(CONTENT).hexdigest()
    def download(self,transport,**overrides):
        args={'base_url':BASE,'job_id':JOB,'token_file':self.auth_file,'sha256':self.digest,'size_bytes':len(CONTENT),'output':self.output}
        args.update(overrides)
        opener=urllib.request.build_opener(fetch.NoRedirectHandler(),transport)
        with mock.patch.object(fetch.urllib.request,'build_opener',return_value=opener):
            return fetch.download(**args)
    def test_download_is_exact_scoped_and_publishes_only_verified_bytes(self):
        transport=Transport()
        self.assertEqual(self.download(transport),self.output)
        self.assertEqual(self.output.read_bytes(),CONTENT)
        request=transport.requests[0]
        self.assertEqual(request.full_url,BASE+'/jobs/'+JOB+'/model-serving/code')
        self.assertEqual(request.get_header('Authorization'),'Bearer '+self.auth_file.read_text())
        self.assertIsNone(request.get_header('Cookie'))
        self.assertEqual(len(transport.requests),1)
    def test_hash_size_truncation_and_oversize_remove_partial_files(self):
        for changes in [{'sha256':'f'*64},{'size_bytes':len(CONTENT)-1},{'size_bytes':len(CONTENT)+1}]:
            with self.assertRaises(fetch.DownloadError):
                self.download(Transport(),**changes)
            self.assertFalse(self.output.exists())
            self.assertEqual([p.name for p in self.root.iterdir()],['job-credential'])
    def test_external_credentialed_redirect_and_malformed_urls_are_rejected(self):
        for base in ['https://external.example/api/v1/internal','http://127.0.0.1/api/v1/internal','https://user:password@backend.platform.svc/api/v1/internal',BASE+'?redirect=x',BASE+'/../admin',BASE+'\n', 'https://backend.platform.svc.evil.example/api/v1/internal']:
            transport=Transport()
            with self.assertRaises(ValueError):
                self.download(transport,base_url=base)
            self.assertFalse(transport.requests)
        for status in [301,302,307,403,503]:
            transport=Transport(self.auth_file.read_bytes(),status,{'Location':'https://other.example/code'})
            with self.assertRaises(fetch.DownloadError) as caught:
                self.download(transport)
            self.assertNotIn(self.auth_file.read_text(),str(caught.exception))
            self.assertEqual(len(transport.requests),1)
    def test_input_limits_and_job_path_injection_fail_before_http(self):
        for changes in [{'size_bytes':0},{'size_bytes':64*1024*1024+1},{'size_bytes':True},{'job_id':'../other'},{'sha256':'bad'},{'sha256':'A'*64}]:
            transport=Transport()
            with self.assertRaises(ValueError):
                self.download(transport,**changes)
            self.assertFalse(transport.requests)
        self.auth_file.write_bytes(b'not-a-job-credential')
        with self.assertRaises(ValueError):
            self.download(Transport())
    def test_existing_target_is_never_overwritten(self):
        self.output.write_bytes(b'existing')
        transport=Transport()
        with self.assertRaises(ValueError):
            self.download(transport)
        self.assertEqual(self.output.read_bytes(),b'existing')
        self.assertFalse(transport.requests)
    def test_network_failure_and_deadline_leave_no_partial_archive(self):
        transport=Transport()
        with mock.patch.object(transport,'https_open',side_effect=OSError('private transport detail')):
            with self.assertRaises(fetch.DownloadError) as caught:
                self.download(transport)
        self.assertNotIn('private transport detail',str(caught.exception))
        with mock.patch.object(fetch.time,'monotonic',side_effect=[0,181]):
            with self.assertRaises(fetch.DownloadError):
                self.download(Transport())
        self.assertFalse(self.output.exists())
        self.assertEqual([p.name for p in self.root.iterdir()],['job-credential'])
        with self.assertRaises(fetch.DownloadError):
            self.download(Transport(),token_file=self.root/'missing-credential')
    def test_interrupted_stream_removes_temporary_archive(self):
        stream=mock.MagicMock()
        stream.code=200
        stream.__enter__.return_value=stream
        stream.read.side_effect=http.client.IncompleteRead(b'partial-private-body')
        opener=mock.Mock()
        opener.open.return_value=stream
        with mock.patch.object(fetch.urllib.request,'build_opener',return_value=opener):
            with self.assertRaises(fetch.DownloadError) as caught:
                fetch.download(base_url=BASE,job_id=JOB,token_file=self.auth_file,sha256=self.digest,size_bytes=len(CONTENT),output=self.output)
        self.assertNotIn('partial-private-body',str(caught.exception))
        self.assertFalse(self.output.exists())
    def test_cli_reports_opaque_error_without_secret_and_does_not_retry(self):
        argv=['--base-url',BASE,'--job-id',JOB,'--token-file',str(self.auth_file),'--sha256',self.digest,'--size-bytes',str(len(CONTENT)),'--output',str(self.output)]
        with mock.patch.object(fetch,'download',return_value=self.output) as call:
            self.assertEqual(fetch.main(argv),0)
            call.assert_called_once()
        with mock.patch.object(fetch,'download',side_effect=fetch.DownloadError('download rejected')),redirect_stderr(io.StringIO()) as output:
            self.assertEqual(fetch.main(argv),1)
            self.assertNotIn(self.auth_file.read_text(),output.getvalue())

if __name__=='__main__':
    unittest.main()
