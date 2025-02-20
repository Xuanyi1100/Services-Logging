import socket
import argparse
import time
import threading
import random

# Network Handler Component
class NetworkClient:
    def __init__(self, host='localhost', port=8080):
        self.sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self.sock.connect((host, port))
    
    def send_message(self, message):
        self.sock.sendall(message.encode())
        try:
            # Add timeout for response
            self.sock.settimeout(1.0)
            response = self.sock.recv(1024)
            return response.decode()
        except socket.timeout:
            return "No response received"
        except Exception as e:
            return f"Error: {str(e)}"

# Test Suite Component
class TestSuite:
    @staticmethod
    def run_concurrency_test(host, port, thread_count):
        def worker():
            client = NetworkClient(host, port)
            # Send 10 valid messages per client
            for i in range(10):
                # Randomly select valid message type
                prefixes = ["INFO:", "WARN:", "ERROR:", "DEBUG:", "AUDIT:"]
                prefix = random.choice(prefixes)
                msg = f"{prefix} Concurrent message {i+1}"
                response = client.send_message(msg)
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
        client = NetworkClient(host, port)
        interval = 1.0 / rate
        
        while True:
            start = time.time()
            response = client.send_message("INFO: Stress test")
            print(f"Response: {response}")
            
            sleep_time = interval - (time.time() - start)
            if sleep_time > 0:
                time.sleep(sleep_time)
            else:
                # Add small delay if we're falling behind
                time.sleep(0.01)

    @staticmethod
    def run_message_types_tests(host, port):
        # Test all message types
        prefixes = ["INFO:", "WARN:", "ERROR:", "DEBUG:", "AUDIT:"]
        for prefix in prefixes:
            client = NetworkClient(host, port)
            response = client.send_message(f"{prefix} Test")
            assert "ACK" in response, f"Failed for {prefix}"
        

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
    parser.add_argument('--client-id', 
                   help='Override default client identifier')
    args = parser.parse_args()

    if args.message:
        client = NetworkClient(args.host, args.port)
        print(client.send_message(args.message))
    elif args.concurrency:
        TestSuite.run_concurrency_test(args.host, args.port, args.concurrency)
    elif args.stress:
        TestSuite.run_stress_test(args.host, args.port, args.stress)
    elif args.messageTypes:
        TestSuite.run_message_types_tests(args.host, args.port)
    else:  # Interactive mode
        client = NetworkClient(args.host, args.port)
        while True:
            msg = input("Enter message: ")
            print(client.send_message(msg + "\n")) 