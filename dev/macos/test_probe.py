import contextlib
import hashlib
import os
from pathlib import Path
import plistlib
import signal
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import probe


class SafetyTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(dir='/private/tmp')
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name) / 'state'

    def test_success_removes_only_owned_run_and_keeps_lock_inode(self):
        other = Path(self.tmp.name) / 'user-file'
        other.write_text('keep')
        with probe.Admission(self.root) as first:
            inode = os.fstat(first.fd).st_ino
            (first.run / 'link').symlink_to(other)
            first.complete()
        with probe.Admission(self.root) as second:
            self.assertEqual(inode, os.fstat(second.fd).st_ino)
            second.complete()
        self.assertEqual(other.read_text(), 'keep')
        self.assertEqual([p.name for p in self.root.iterdir()], ['lock'])

    def test_second_live_admission_refused(self):
        with probe.Admission(self.root) as active:
            with self.assertRaises(BlockingIOError):
                with probe.Admission(self.root):
                    self.fail('second admitted')
            active.complete()

    def test_failure_quarantines_and_blocks_next_launch(self):
        with probe.Admission(self.root) as active:
            work = active.run
        self.assertTrue(work.is_dir())
        with self.assertRaisesRegex(probe.SafetyError, 'unresolved'):
            with probe.Admission(self.root):
                pass

    def test_crashed_controller_does_not_clear_barrier(self):
        script = 'import os,sys; from pathlib import Path; import probe; a=probe.Admission(Path(sys.argv[1])); a.__enter__(); os._exit(9)'
        child = subprocess.Popen([sys.executable, '-c', script, str(self.root)], cwd=probe.HERE)
        self.assertEqual(child.wait(timeout=5), 9)
        with self.assertRaisesRegex(probe.SafetyError, 'unresolved'):
            with probe.Admission(self.root):
                pass

    def test_inherited_lock_survives_controller_fd_close(self):
        with probe.Admission(self.root) as active:
            child = subprocess.Popen([sys.executable, '-c', 'import sys; sys.stdin.read()'],
                                     stdin=subprocess.PIPE, pass_fds=(active.fd,))
            self.addCleanup(lambda: probe.stop_child(child))
        # Remove only the synthetic marker to independently prove inherited flock.
        (self.root / 'active.json').unlink()
        with self.assertRaises(BlockingIOError):
            with probe.Admission(self.root):
                pass
        child.stdin.close()
        child.wait(timeout=5)
        # Even without the marker, the leftover preparation blocks admission.
        with self.assertRaisesRegex(probe.SafetyError, 'artifacts'):
            with probe.Admission(self.root):
                pass

    def test_symlink_root_refused(self):
        target = Path(self.tmp.name) / 'outside'
        target.mkdir(mode=0o700)
        self.root.symlink_to(target)
        with self.assertRaises(probe.SafetyError):
            with probe.Admission(self.root):
                pass
        self.assertEqual(list(target.iterdir()), [])

    def test_shared_permissions_refused(self):
        self.root.mkdir(mode=0o755)
        with self.assertRaises(probe.SafetyError):
            with probe.Admission(self.root):
                pass

    def test_symlink_and_hardlink_lock_refused(self):
        self.root.mkdir(mode=0o700)
        other = Path(self.tmp.name) / 'other'
        other.write_text('keep')
        other.chmod(0o600)
        for link in ('symlink', 'hardlink'):
            with self.subTest(link=link):
                lock = self.root / 'lock'
                if link == 'symlink': lock.symlink_to(other)
                else: os.link(other, lock)
                with self.assertRaises((probe.SafetyError, OSError)):
                    with probe.Admission(self.root): pass
                lock.unlink()
                self.assertEqual(other.read_text(), 'keep')

    def test_copy_uses_verified_bytes(self):
        source = Path(self.tmp.name) / 'input'
        destination = Path(self.tmp.name) / 'output'
        source.write_bytes(b'kernel')
        probe.copy_pinned(source, destination, hashlib.sha256(b'kernel').hexdigest())
        self.assertEqual(destination.read_bytes(), b'kernel')
        self.assertEqual(stat.S_IMODE(destination.stat().st_mode), 0o600)

    def test_copy_wrong_digest_rejected(self):
        source = Path(self.tmp.name) / 'input'; source.write_bytes(b'kernel')
        with self.assertRaisesRegex(probe.SafetyError, 'mismatch'):
            probe.copy_pinned(source, Path(self.tmp.name) / 'out', '0' * 64)

    def test_oversized_source_rejected_before_copy(self):
        source = Path(self.tmp.name) / 'input'
        with source.open('wb') as f: f.truncate(probe.INPUT_LIMIT + 1)
        with self.assertRaises(probe.SafetyError):
            probe.copy_pinned(source, Path(self.tmp.name) / 'out', '0' * 64)
        self.assertFalse((Path(self.tmp.name) / 'out').exists())

    def test_fifo_source_does_not_block(self):
        source = Path(self.tmp.name) / 'input'; os.mkfifo(source)
        with self.assertRaises(probe.SafetyError):
            probe.copy_pinned(source, Path(self.tmp.name) / 'out', '0' * 64)

    def test_symlink_source_rejected(self):
        source = Path(self.tmp.name) / 'input'; source.symlink_to('/dev/zero')
        with self.assertRaises(probe.SafetyError):
            probe.copy_pinned(source, Path(self.tmp.name) / 'out', '0' * 64)

    def test_command_deadline_does_not_kill_unrelated_child(self):
        unrelated = subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(20)'])
        try:
            with self.assertRaisesRegex(probe.SafetyError, 'deadline'):
                probe.bounded_command([sys.executable, '-c', 'import time; time.sleep(20)'], seconds=.1)
            self.assertIsNone(unrelated.poll())
        finally:
            probe.stop_child(unrelated)

    def test_command_output_bounded(self):
        with self.assertRaisesRegex(probe.SafetyError, 'output exceeded'):
            probe.bounded_command([sys.executable, '-c', 'import os; os.write(1,b"x"*2000000)'])

    def test_command_failure_not_success(self):
        with self.assertRaisesRegex(probe.SafetyError, 'command failed'):
            probe.bounded_command([sys.executable, '-c', 'raise SystemExit(7)'])

    def test_command_keeps_plist_stdout_separate_from_warning_stderr(self):
        document = plistlib.dumps({'system-entities': []})
        for warning_first in (False, True):
            with self.subTest(warning_first=warning_first):
                writes = ['os.write(1, ' + repr(document) + ')',
                          'os.write(2, b"hdiutil warning\\n")']
                if warning_first:
                    writes.reverse()
                script = 'import os; ' + '; '.join(writes)
                result = probe.bounded_command([sys.executable, '-c', script])
                self.assertEqual(result, document)
                self.assertEqual(plistlib.loads(result), {'system-entities': []})

    def test_command_drains_both_streams_without_deadlock(self):
        script = 'import os\nfor i in range(32):\n os.write(1,b"a"*4096)\n os.write(2,b"b"*4096)'
        self.assertEqual(probe.bounded_command([sys.executable, '-c', script]), b'a' * 131072)

    def test_stderr_and_combined_output_share_one_bound(self):
        for writes in ['os.write(2,b"e"*2048)',
                       'os.write(1,b"a"*600); os.write(2,b"e"*600)']:
            with self.subTest(writes=writes), patch.object(probe, 'OUTPUT_LIMIT', 1024):
                with self.assertRaisesRegex(probe.SafetyError, 'output exceeded'):
                    probe.bounded_command([sys.executable, '-c', 'import os; ' + writes])

    def test_command_drains_remaining_pipe_after_stdout_closes(self):
        result = probe.bounded_command([sys.executable, '-c',
            'import os; os.close(1); os.write(2,b"warning"*20000)'])
        self.assertEqual(result, b'')

    def test_command_drains_stdout_after_stderr_closes(self):
        result = probe.bounded_command([sys.executable, '-c',
            'import os; os.close(2); os.write(1,b"out"*20000)'])
        self.assertEqual(result, b'out' * 20000)

    def test_malformed_stdout_is_not_repaired_by_stream_separation(self):
        with self.assertRaises(plistlib.InvalidFileException):
            with probe.Admission(self.root):
                raw = probe.bounded_command([sys.executable, '-c',
                    'import os; os.write(1,b"not a plist"); os.write(2,b"warning")'])
                plistlib.loads(raw)
        self.assertTrue((self.root / 'active.json').exists())
        with self.assertRaisesRegex(probe.SafetyError, 'unresolved'):
            with probe.Admission(self.root):
                self.fail('malformed output allowed a new run')

    def test_stderr_overflow_preserves_admission_quarantine(self):
        with probe.Admission(self.root):
            with patch.object(probe, 'OUTPUT_LIMIT', 1024):
                with self.assertRaisesRegex(probe.SafetyError, 'output exceeded'):
                    probe.bounded_command([sys.executable, '-c',
                        'import os; os.write(2,b"e"*2048)'])
        with self.assertRaisesRegex(probe.SafetyError, 'unresolved'):
            with probe.Admission(self.root):
                self.fail('overflow allowed a new run')

    def test_closed_output_pipes_do_not_disable_pressure_monitor(self):
        calls = []
        def monitor():
            calls.append(True)
            if len(calls) >= 2:
                raise probe.SafetyError('pressure monitor active')
        with self.assertRaisesRegex(probe.SafetyError, 'pressure monitor active'):
            probe.bounded_command([sys.executable, '-c',
                'import os,time; os.close(1); os.close(2); time.sleep(5)'], seconds=.5, tick=monitor)

    def test_closed_output_pipes_still_report_safety_deadline(self):
        with self.assertRaisesRegex(probe.SafetyError, 'deadline'):
            probe.bounded_command([sys.executable, '-c',
                'import os,time; os.close(1); os.close(2); time.sleep(5)'], seconds=.2)

    def test_failure_retains_labelled_stdout_and_stderr_diagnostics(self):
        with self.assertRaisesRegex(probe.SafetyError, 'stdout: out.*stderr: diagnostic'):
            probe.bounded_command([sys.executable, '-c',
                'import os; os.write(1,b"out"); os.write(2,b"diagnostic"); raise SystemExit(7)'])

    def test_ignored_term_is_killed_only_as_owned_child(self):
        script = 'import signal,time; signal.signal(signal.SIGTERM,signal.SIG_IGN); print("ready",flush=True); time.sleep(20)'
        with self.assertRaisesRegex(probe.SafetyError, 'deadline'):
            probe.bounded_command([sys.executable, '-c', script], seconds=.3)

    def test_watchdog_error_quarantines(self):
        def failure(): raise probe.SafetyError('pressure')
        with probe.Admission(self.root):
            with self.assertRaisesRegex(probe.SafetyError, 'pressure'):
                probe.bounded_command([sys.executable, '-c', 'import time; time.sleep(20)'], tick=failure)
        self.assertTrue((self.root / 'active.json').exists())

    def test_memory_parser_fails_closed(self):
        self.assertEqual(probe.memory_available('page size of 16384 bytes\nPages free: 3.\nPages speculative: 2.'), 5*16384)
        with self.assertRaises(probe.SafetyError): probe.memory_available('unexpected')

    def test_record_is_bounded(self):
        with self.assertRaises(probe.SafetyError):
            probe.write_record(Path(self.tmp.name)/'large.json', {'data': 'x'*5000})

    def test_image_identity_change_never_detaches(self):
        calls = []
        image = probe.Image(Path(self.tmp.name), lambda cmd, **kw: calls.append(cmd) or plistlib.dumps({'images': []}))
        image.device = '/dev/disk999'
        with self.assertRaisesRegex(probe.SafetyError, 'identity'):
            image.detach()
        self.assertFalse(any('detach' in c for c in calls))

    def test_matching_image_wrong_device_never_detaches(self):
        calls=[]
        image=probe.Image(Path(self.tmp.name), None); image.device='/dev/disk999'
        info={'images':[{'image-path':str(image.path),'system-entities':[{'dev-entry':'/dev/disk998','mount-point':str(image.mount)}]}]}
        image.command=lambda cmd,**kw:calls.append(cmd) or plistlib.dumps(info)
        with self.assertRaises(probe.SafetyError): image.detach()
        self.assertFalse(any('detach' in c for c in calls))

    def test_detach_never_forced_and_success_rechecked(self):
        calls=[]
        image=probe.Image(Path(self.tmp.name), None); image.device='/dev/disk999'
        info={'images':[{'image-path':str(image.path),'system-entities':[{'dev-entry':image.device,'mount-point':str(image.mount)}]}]}
        results=iter([plistlib.dumps(info),b'',plistlib.dumps({'images':[]})])
        image.command=lambda cmd,**kw:calls.append(cmd) or next(results)
        with patch.object(probe.os.path,'ismount',return_value=False): image.detach()
        self.assertEqual(calls[1],['/usr/bin/hdiutil','detach',str(image.mount)])
        self.assertNotIn('-force',sum(calls,[]))

    def test_reported_detach_success_with_live_mount_rejected(self):
        image=probe.Image(Path(self.tmp.name),None); image.device='/dev/disk999'
        info={'images':[{'image-path':str(image.path),'system-entities':[{'dev-entry':image.device,'mount-point':str(image.mount)}]}]}
        results=iter([plistlib.dumps(info),b'',plistlib.dumps({'images':[]})])
        image.command=lambda *a,**kw:next(results)
        with patch.object(probe.os.path,'ismount',return_value=True),self.assertRaises(probe.SafetyError): image.detach()


if __name__ == '__main__': unittest.main()
