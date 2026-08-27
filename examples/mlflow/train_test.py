import os
import unittest
from unittest import mock

import train


class PlatformContractTest(unittest.TestCase):
    def test_tags_use_platform_injected_identity(self):
        values = {
            "RAYTRAIN_JOB_ID": "job-123",
            "RAYTRAIN_TENANT_ID": "local",
            "RAYTRAIN_SUBMITTER_USER_ID": "user-456",
            "RAYTRAIN_MLFLOW_PROVENANCE": "signed-provenance",
            "RAYTRAIN_CLUSTER_ATTEMPT": "2",
        }
        with mock.patch.dict(os.environ, values, clear=True):
            self.assertEqual(
                train.platform_tags(),
                {
                    "platform.job_id": "job-123",
                    "platform.tenant_id": "local",
                    "platform.submitter_user_id": "user-456",
                    "platform.provenance": "signed-provenance",
                    "platform.cluster_attempt": "2",
                },
            )

    def test_missing_job_identity_fails_fast(self):
        with mock.patch.dict(os.environ, {}, clear=True):
            with self.assertRaisesRegex(RuntimeError, "RAYTRAIN_JOB_ID"):
                train.platform_tags()


if __name__ == "__main__":
    unittest.main()
