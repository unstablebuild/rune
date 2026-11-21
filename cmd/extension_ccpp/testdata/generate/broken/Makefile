# The build needs a generated header that no rule makes, so make stops
# before it prints a single compile command.
app: main.o gen.h
	$(CC) -o app main.o

main.o: main.c
	$(CC) -c main.c -o main.o
