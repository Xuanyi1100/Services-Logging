"""

Log Server Test Client

Provides testing utilities for load testing and validating server behavior.
Supports concurrency, stress testing, and invalid message validation.
"""

import socket
import argparse
import time
import threading
import random
import json

MESSAGE_COUNT = 10      # For concurrency test. The number of messages to send in each thread
RESPONSE_TIMEOUT = 1.0  # Timeout for response in seconds
INVALID_SOURCE = "a" * 257
LONG_MESSAGE = "a" * 1025
FUTURE_TIME_OFFSET = 3600*24
PAST_TIME_OFFSET = 3600*24*365

# Network Handler Component
class NetworkClient:
    """TCP client for sending structured log messages to server"""
    
    def __init__(self, host='localhost', port=8080, client_id=None):
        """Initialize connection to log server"""
        self.client_id = client_id or socket.gethostname()
        self.sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self.sock.connect((host, port))
    
    def send_message(self, message, level="INFO", timestamp=time.time(), source=None):
        """
        Send structured log message to server
        
        Args:
            message: Content text
            level: Log severity level
            timestamp: Optional epoch timestamp
            source: Source identifier
            
        Returns:
            Server response or error message
        """
        if(source is None):
            source = self.client_id
        json_msg = {
            "level": level,
            "message": message,
            "timestamp": timestamp,
            "source": source
        }
        # Serialize and send
        self.sock.sendall(json.dumps(json_msg).encode())
        try:
            # Add timeout for response
            self.sock.settimeout(RESPONSE_TIMEOUT)
            response = self.sock.recv(1024)
            return response.decode()
        except socket.timeout:
            return f"No response received within {RESPONSE_TIMEOUT} seconds"
        except Exception as e:
            return f"Error: {str(e)}"

# Test Suite Component
class TestSuite:
    """Collection of server test scenarios"""
    
    @staticmethod
    def run_concurrency_test(host, port, thread_count):
        """Test concurrent client connections
        Args:
            thread_count: Number of simultaneous clients
        """
        def worker():
            client = NetworkClient(host, port)
            # Send 10 valid messages per client
            for i in range(MESSAGE_COUNT):
                
                msg = f" Concurrent message {i+1}"
                response = client.send_message(msg,"INFO")
                print(f"Response: {response.strip()}")
        
        threads = []
        for _ in range(thread_count):
            t = threading.Thread(target=worker)
            threads.append(t)
            t.start()
        
        for t in threads:
            t.join()
    

    @staticmethod
    def run_stress_test(host, port, rate):
        """Sustained high-volume message test
        Args:
            rate: Target messages per second
        """
        client = NetworkClient(host, port)
        interval = 1.0 / rate
        count = 0
        while True:
            start = time.time()
            response = client.send_message(f"Stress test {count}","INFO")
            print(f"Response: {response}")
            count += 1
            sleep_time = interval - (time.time() - start)
            if sleep_time > 0:
                time.sleep(sleep_time)
            else:
                # Add small delay if we're falling behind
                time.sleep(0.01)

    @staticmethod
    def run_message_types_tests(host, port):
        """Validate all supported log level types"""
        # Test all message types
        prefixes = ["INFO", "WARN", "ERROR", "DEBUG", "AUDIT"]
        for prefix in prefixes:
            client = NetworkClient(host, port)
            response = client.send_message("Message Type Test",prefix)
            assert "ACK" in response, f"Failed for {prefix}"
        
    @staticmethod
    def run_invalid_message_test(host, port):
        """Verify server rejection of invalid messages"""
        client = NetworkClient(host, port)
        # Send invalid message
        msg = f"A invalid long message {LONG_MESSAGE}"
        print(f"Sending {msg}")
        response = client.send_message(msg)
        print(f"Response: {response}")

        # Send message with future timestamp
        future_timestamp = int(time.time()) + FUTURE_TIME_OFFSET
        msg = f"Future timestamp {future_timestamp}"
        print(f"Sending {msg}")
        response = client.send_message(msg,"INFO", future_timestamp)
        print(f"Response: {response}")

        # Send message with past timestamp
        past_timestamp = int(time.time()) - PAST_TIME_OFFSET
        msg = f"Past timestamp {past_timestamp}"
        print(f"Sending {msg}")
        response = client.send_message(msg,"INFO", past_timestamp)
        print(f"Response: {response}")

        # Send message with invalid source
        msg = f"Invalid source {INVALID_SOURCE}"
        print(f"Sending {msg}")
        response = client.send_message(msg,"INFO", source=INVALID_SOURCE)
        print(f"Response: {response}")

if __name__ == "__main__":
    # Add command-line arguments
    parser = argparse.ArgumentParser()
    parser.add_argument('--message', help='Direct message to send')
    parser.add_argument('--concurrency', type=int,
                       metavar='THREAD_COUNT',
                       help='Number of concurrent clients to test')
    parser.add_argument('--messageTypes', action='store_true',
                   help='Run log message types tests')
    parser.add_argument('--host', default='localhost',
                   help='Server hostname')
    parser.add_argument('--port', type=int, default=8080,
                   help='Server port')
    parser.add_argument('--stress', type=int,
                   help='Messages per second for stress test')
    parser.add_argument('--invalidMessage', action='store_true',
                   help='Send invalid message')
    args = parser.parse_args()

    if args.message:
        client = NetworkClient(args.host, args.port)
        print(client.send_message(args.message,"INFO"))
    elif args.concurrency:
        TestSuite.run_concurrency_test(args.host, args.port, args.concurrency)
    elif args.stress:
        TestSuite.run_stress_test(args.host, args.port, args.stress)
    elif args.messageTypes:
        TestSuite.run_message_types_tests(args.host, args.port)
    elif args.invalidMessage:
        TestSuite.run_invalid_message_test(args.host, args.port)
    else:  # Interactive mode
        client = NetworkClient(args.host, args.port)
        while True:
            msg = input("Enter message: ")
            print(client.send_message(msg + "\n","INFO")) 