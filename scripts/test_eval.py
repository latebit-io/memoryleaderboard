import unittest

import eval as local_eval


class EvalHelpersTest(unittest.TestCase):
    def test_content_items_skip_malformed_records(self) -> None:
        body = {"data": [None, {"content": None}, {"content": "valid"}]}
        self.assertEqual(local_eval.content_items(body), [{"content": "valid"}])

    def test_metric_cutoff_label(self) -> None:
        self.assertEqual(local_eval.metric_cutoff_label([{}, {}], 5), "5")
        self.assertEqual(local_eval.metric_cutoff_label([{}, {"top_k": 3}], 5), "mixed")


if __name__ == "__main__":
    unittest.main()
