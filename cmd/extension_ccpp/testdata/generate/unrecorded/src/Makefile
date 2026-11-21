CFLAGS = -DFROM_MAKE=1

app: main.o
	$(CC) -o app main.o

main.o: main.c
	$(CC) $(CFLAGS) -c main.c -o main.o
